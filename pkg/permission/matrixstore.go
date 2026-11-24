package permission

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// ContextPolicyLoader reads every entity-level policy of one tenant: the rows FindContextPolicies
// would return (no field, subject "context"), for all entities, actions and resources at once.
type ContextPolicyLoader func(ctx context.Context, tenantID uuid.UUID) ([]PolicyRow, error)

// MatrixStore is a PolicyStore that answers FindContextPolicies from memory (roadmap 120).
//
// The entity-level check (may this caller's roles do this action on this entity or resource) reads
// only these rows and the caller's own context, never a record, so a tenant's whole set can be
// held in memory: one load per tenant per TTL instead of one query per check. The decision logic
// is unchanged (checkAction, CheckResource, CanTransitionAction evaluate the same rows the same
// way); only where the rows come from changes.
//
// A policy written through this store (CreatePolicy, DeletePolicy, SyncBasicPolicies) drops the
// tenant's entry at once, so a change applies immediately in this process. A change written any
// other way, or in another process, applies within the TTL. A failed load is an error, never an
// empty set, so the check fails closed.
//
// Every other PolicyStore method goes straight to the wrapped store: record- and field-level
// policies stay with the snapshot, which is loaded only for entities that need it.
type MatrixStore struct {
	PolicyStore
	load ContextPolicyLoader
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	tenants map[uuid.UUID]*matrixEntry
	group   singleflight.Group
}

type matrixEntry struct {
	byKey    map[matrixKey][]PolicyRow
	loadedAt time.Time
}

type matrixKey struct{ entity, action string }

// NewMatrixStore wraps inner. A ttl of zero or less disables the matrix: inner is returned as is.
func NewMatrixStore(inner PolicyStore, load ContextPolicyLoader, ttl time.Duration) PolicyStore {
	if ttl <= 0 || load == nil {
		return inner
	}
	return &MatrixStore{PolicyStore: inner, load: load, ttl: ttl, now: time.Now, tenants: map[uuid.UUID]*matrixEntry{}}
}

// FindContextPolicies returns the tenant's rows for (entity, action) from the in-memory matrix.
func (m *MatrixStore) FindContextPolicies(ctx context.Context, tenantID uuid.UUID, entity, action string) ([]PolicyRow, error) {
	e, err := m.entry(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return append([]PolicyRow(nil), e.byKey[matrixKey{entity, action}]...), nil
}

// InvalidateTenant drops a tenant's matrix so the next check reloads it. Safe to call at any time.
func (m *MatrixStore) InvalidateTenant(tenantID uuid.UUID) {
	m.mu.Lock()
	delete(m.tenants, tenantID)
	m.mu.Unlock()
}

func (m *MatrixStore) entry(ctx context.Context, tenantID uuid.UUID) (*matrixEntry, error) {
	if e := m.fresh(tenantID); e != nil {
		return e, nil
	}
	v, err, _ := m.group.Do(tenantID.String(), func() (any, error) {
		if e := m.fresh(tenantID); e != nil {
			return e, nil
		}
		rows, err := m.load(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		e := &matrixEntry{byKey: make(map[matrixKey][]PolicyRow, len(rows)), loadedAt: m.now()}
		for _, r := range rows {
			k := matrixKey{r.Entity, r.Action}
			e.byKey[k] = append(e.byKey[k], r)
		}
		m.mu.Lock()
		m.tenants[tenantID] = e
		m.mu.Unlock()
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*matrixEntry), nil
}

func (m *MatrixStore) fresh(tenantID uuid.UUID) *matrixEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.tenants[tenantID]
	if e == nil || m.now().Sub(e.loadedAt) >= m.ttl {
		return nil
	}
	return e
}

func (m *MatrixStore) CreatePolicy(ctx context.Context, tenantID uuid.UUID, entity, action string, roles []string, hasRoles bool,
	condition *PolicyCondition, createdBy *uuid.UUID, field string, subject *PolicySubject, effect PolicyEffect) (PolicyRow, error) {
	row, err := m.PolicyStore.CreatePolicy(ctx, tenantID, entity, action, roles, hasRoles, condition, createdBy, field, subject, effect)
	m.InvalidateTenant(tenantID)
	return row, err
}

func (m *MatrixStore) DeletePolicy(ctx context.Context, tenantID, id uuid.UUID) error {
	err := m.PolicyStore.DeletePolicy(ctx, tenantID, id)
	m.InvalidateTenant(tenantID)
	return err
}

func (m *MatrixStore) SyncBasicPolicies(ctx context.Context, tenantID uuid.UUID, entity string, grants []RoleActionGrant, createdBy *uuid.UUID) ([]PolicyRow, error) {
	rows, err := m.PolicyStore.SyncBasicPolicies(ctx, tenantID, entity, grants, createdBy)
	m.InvalidateTenant(tenantID)
	return rows, err
}
