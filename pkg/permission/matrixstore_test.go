package permission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// loaderStore is the wrapped store: it records the writes it receives and what the loader read.
type loaderStore struct {
	emptyStore
	rows    map[uuid.UUID][]PolicyRow
	loads   int
	err     error
	created int
	deleted int
	synced  int
}

func (s *loaderStore) load(_ context.Context, tenant uuid.UUID) ([]PolicyRow, error) {
	s.loads++
	if s.err != nil {
		return nil, s.err
	}
	return s.rows[tenant], nil
}

func (s *loaderStore) CreatePolicy(context.Context, uuid.UUID, string, string, []string, bool, *PolicyCondition, *uuid.UUID, string, *PolicySubject, PolicyEffect) (PolicyRow, error) {
	s.created++
	return PolicyRow{}, nil
}
func (s *loaderStore) DeletePolicy(context.Context, uuid.UUID, uuid.UUID) error {
	s.deleted++
	return nil
}
func (s *loaderStore) SyncBasicPolicies(context.Context, uuid.UUID, string, []RoleActionGrant, *uuid.UUID) ([]PolicyRow, error) {
	s.synced++
	return nil, nil
}

func policy(entity, action string, effect PolicyEffect, roles ...string) PolicyRow {
	return PolicyRow{Entity: entity, Action: action, Roles: roles, HasRoles: len(roles) > 0, Effect: effect}
}

func newMatrix(t *testing.T, s *loaderStore, ttl time.Duration) (*MatrixStore, *time.Time) {
	t.Helper()
	store := NewMatrixStore(s, s.load, ttl)
	m, ok := store.(*MatrixStore)
	require.True(t, ok)
	clock := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	return m, &clock
}

func TestMatrixDisabledReturnsTheWrappedStore(t *testing.T) {
	s := &loaderStore{}
	require.Same(t, s, NewMatrixStore(s, s.load, 0))
	require.Same(t, s, NewMatrixStore(s, nil, time.Minute))
}

func TestMatrixLoadsATenantOncePerTTLAndAnswersByEntityAndAction(t *testing.T) {
	tenant := uuid.New()
	s := &loaderStore{rows: map[uuid.UUID][]PolicyRow{tenant: {
		policy("waf.zones", "read", EffectAllow, "viewer"),
		policy("waf.zones", "update", EffectAllow, "analyst"),
		policy("waf.zones", "update", EffectDeny, "intern"),
	}}}
	m, _ := newMatrix(t, s, 45*time.Second)

	for range 4 {
		read, err := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
		require.NoError(t, err)
		require.Len(t, read, 1)
	}
	update, _ := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "update")
	require.Len(t, update, 2)
	none, err := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "delete")
	require.NoError(t, err)
	require.Empty(t, none, "no policy for an action means no rows, which the check turns into a deny")
	require.Equal(t, 1, s.loads)
}

func TestMatrixKeepsTenantsApart(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	s := &loaderStore{rows: map[uuid.UUID][]PolicyRow{
		a: {policy("waf.zones", "read", EffectAllow, "viewer")},
	}}
	m, _ := newMatrix(t, s, time.Minute)

	rowsA, _ := m.FindContextPolicies(context.Background(), a, "waf.zones", "read")
	rowsB, _ := m.FindContextPolicies(context.Background(), b, "waf.zones", "read")
	require.Len(t, rowsA, 1)
	require.Empty(t, rowsB, "tenant b has no policies; it must not see tenant a's")
	require.Equal(t, 2, s.loads)
}

func TestMatrixReloadsAfterTheTTL(t *testing.T) {
	tenant := uuid.New()
	s := &loaderStore{rows: map[uuid.UUID][]PolicyRow{tenant: {policy("waf.zones", "read", EffectAllow, "viewer")}}}
	m, clock := newMatrix(t, s, 45*time.Second)

	_, _ = m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	*clock = clock.Add(44 * time.Second)
	_, _ = m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	require.Equal(t, 1, s.loads, "still inside the TTL")

	s.rows[tenant] = nil // an admin removed the grant somewhere else
	*clock = clock.Add(2 * time.Second)
	rows, _ := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	require.Empty(t, rows, "a change made elsewhere is seen once the entry is older than the TTL")
	require.Equal(t, 2, s.loads)
}

func TestMatrixFailedLoadIsAnErrorAndNotCached(t *testing.T) {
	tenant := uuid.New()
	boom := errors.New("db down")
	s := &loaderStore{err: boom}
	m, _ := newMatrix(t, s, time.Minute)

	_, err := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	require.ErrorIs(t, err, boom, "a failed load must not read as 'no policies'")

	s.err = nil
	s.rows = map[uuid.UUID][]PolicyRow{tenant: {policy("waf.zones", "read", EffectAllow, "viewer")}}
	rows, err := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestMatrixWritesGoThroughAndDropTheTenant(t *testing.T) {
	tenant := uuid.New()
	s := &loaderStore{rows: map[uuid.UUID][]PolicyRow{tenant: {policy("waf.zones", "read", EffectAllow, "viewer")}}}
	m, _ := newMatrix(t, s, time.Hour)
	ctx := context.Background()

	_, _ = m.FindContextPolicies(ctx, tenant, "waf.zones", "read")
	_, err := m.CreatePolicy(ctx, tenant, "waf.zones", "read", nil, false, nil, nil, "", nil, EffectAllow)
	require.NoError(t, err)
	_, _ = m.FindContextPolicies(ctx, tenant, "waf.zones", "read")
	require.Equal(t, 2, s.loads, "CreatePolicy dropped the matrix")

	require.NoError(t, m.DeletePolicy(ctx, tenant, uuid.New()))
	_, _ = m.FindContextPolicies(ctx, tenant, "waf.zones", "read")
	require.Equal(t, 3, s.loads, "DeletePolicy dropped the matrix")

	_, err = m.SyncBasicPolicies(ctx, tenant, "waf.zones", nil, nil)
	require.NoError(t, err)
	_, _ = m.FindContextPolicies(ctx, tenant, "waf.zones", "read")
	require.Equal(t, 4, s.loads, "SyncBasicPolicies dropped the matrix")

	require.Equal(t, 1, s.created)
	require.Equal(t, 1, s.deleted)
	require.Equal(t, 1, s.synced)
}

func TestMatrixCallerCannotChangeWhatTheNextCheckReads(t *testing.T) {
	tenant := uuid.New()
	s := &loaderStore{rows: map[uuid.UUID][]PolicyRow{tenant: {policy("waf.zones", "read", EffectAllow, "viewer")}}}
	m, _ := newMatrix(t, s, time.Minute)

	first, _ := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	first[0].Effect = EffectDeny
	second, _ := m.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
	require.Equal(t, EffectAllow, second[0].Effect)
}

// The decision itself is unchanged by where the rows come from: the same check, with and without
// the matrix, gives the same answer for allow, deny and no policy.
func TestPermissionDecisionsAreTheSameThroughTheMatrix(t *testing.T) {
	tenant := uuid.New()
	rows := map[uuid.UUID][]PolicyRow{tenant: {
		policy("waf.zones", "read", EffectAllow, "viewer", "analyst"),
		policy("waf.zones", "update", EffectAllow, "analyst"),
		policy("waf.zones", "update", EffectDeny, "intern"),
	}}
	s := &loaderStore{rows: rows}
	direct := NewPermissionService(directStore{rows: rows})
	viaMatrix := NewPermissionService(NewMatrixStore(s, s.load, time.Minute))

	cases := []struct {
		name  string
		roles []string
		check func(*PermissionService, *RequestContext) (PermissionDecision, error)
	}{
		{"viewer reads", []string{"viewer"}, func(p *PermissionService, rc *RequestContext) (PermissionDecision, error) {
			return p.CanReadEntity(context.Background(), rc, "waf.zones")
		}},
		{"viewer cannot update", []string{"viewer"}, func(p *PermissionService, rc *RequestContext) (PermissionDecision, error) {
			return p.CanUpdateEntity(context.Background(), rc, "waf.zones")
		}},
		{"analyst updates", []string{"analyst"}, func(p *PermissionService, rc *RequestContext) (PermissionDecision, error) {
			return p.CanUpdateEntity(context.Background(), rc, "waf.zones")
		}},
		{"deny wins over allow", []string{"analyst", "intern"}, func(p *PermissionService, rc *RequestContext) (PermissionDecision, error) {
			return p.CanUpdateEntity(context.Background(), rc, "waf.zones")
		}},
		{"no policy is a deny", []string{"viewer"}, func(p *PermissionService, rc *RequestContext) (PermissionDecision, error) {
			return p.CanDeleteEntity(context.Background(), rc, "waf.zones")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := &RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: c.roles, HasRoles: true}
			want, err := c.check(direct, rc)
			require.NoError(t, err)
			got, err := c.check(viaMatrix, rc)
			require.NoError(t, err)
			require.Equal(t, want.Allowed, got.Allowed)
		})
	}
}

// directStore answers FindContextPolicies from a map, the way the Postgres store would.
type directStore struct {
	emptyStore
	rows map[uuid.UUID][]PolicyRow
}

func (d directStore) FindContextPolicies(_ context.Context, tenant uuid.UUID, entity, action string) ([]PolicyRow, error) {
	var out []PolicyRow
	for _, r := range d.rows[tenant] {
		if r.Entity == entity && r.Action == action {
			out = append(out, r)
		}
	}
	return out, nil
}
