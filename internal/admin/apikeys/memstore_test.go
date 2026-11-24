package apikeys

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// memStore is the Store the unit tests use. It also plays the part of the rest of the system that
// makes members exist (member), which the contract needs.
type memStore struct {
	mu      sync.Mutex
	members map[[2]uuid.UUID]bool // (tenant, member)
	support map[[2]uuid.UUID]bool
	keys    []*identity.APIKey
}

func newMemStore() *memStore {
	return &memStore{members: map[[2]uuid.UUID]bool{}, support: map[[2]uuid.UUID]bool{}}
}

func (m *memStore) member(tenant uuid.UUID) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.New()
	m.members[[2]uuid.UUID{tenant, id}] = true
	return id
}

func (m *memStore) supportMember(tenant uuid.UUID) uuid.UUID {
	id := m.member(tenant)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.support[[2]uuid.UUID{tenant, id}] = true
	return id
}

func (m *memStore) Create(_ context.Context, tenant, member uuid.UUID, name string, expires *time.Time, createdBy *uuid.UUID, now time.Time) (string, *identity.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.members[[2]uuid.UUID{tenant, member}] {
		return "", nil, identity.ErrNotFound
	}
	token := identity.APIKeyPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	k := &identity.APIKey{ID: uuid.New(), TenantID: tenant, MemberID: member, Name: strings.TrimSpace(name),
		Prefix: token[:len(identity.APIKeyPrefix)+5], ExpiresAt: expires, CreatedBy: createdBy, CreatedAt: now}
	m.keys = append(m.keys, k)
	copy := *k
	return token, &copy, nil
}

func (m *memStore) List(_ context.Context, tenant, member uuid.UUID) ([]identity.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []identity.APIKey{}
	for _, k := range m.keys {
		if k.TenantID == tenant && k.MemberID == member {
			out = append(out, *k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memStore) find(tenant, key uuid.UUID) *identity.APIKey {
	for _, k := range m.keys {
		if k.ID == key && k.TenantID == tenant {
			return k
		}
	}
	return nil
}

func (m *memStore) KeyMember(_ context.Context, tenant, key uuid.UUID) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k := m.find(tenant, key); k != nil {
		return k.MemberID, nil
	}
	return uuid.Nil, identity.ErrNotFound
}

func (m *memStore) Revoke(_ context.Context, tenant, key uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.find(tenant, key)
	if k == nil {
		return identity.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = &now
	}
	return nil
}

func (m *memStore) IsSupportSession(_ context.Context, tenant, member uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.support[[2]uuid.UUID{tenant, member}], nil
}
