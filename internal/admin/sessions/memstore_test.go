package sessions

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// memStore is the Store the unit tests use. It also plays the part of the rest of the system that
// makes people and sessions exist (person, signIn), which the contract needs.
type memStore struct {
	mu         sync.Mutex
	identities map[[2]uuid.UUID]uuid.UUID // (tenant, member) -> identity
	sessions   map[uuid.UUID]*memSession
}

type memSession struct {
	identity.Session
	revoked bool
}

func newMemStore() *memStore {
	return &memStore{identities: map[[2]uuid.UUID]uuid.UUID{}, sessions: map[uuid.UUID]*memSession{}}
}

func (m *memStore) person(tenant uuid.UUID) (member uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	member = uuid.New()
	m.identities[[2]uuid.UUID{tenant, member}] = uuid.New()
	return member
}

func (m *memStore) signIn(tenant, member uuid.UUID, ttl time.Duration, now time.Time) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.New()
	m.sessions[id] = &memSession{Session: identity.Session{
		ID: id, IdentityID: m.identities[[2]uuid.UUID{tenant, member}], MemberID: member, TenantID: tenant,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(ttl), IP: "203.0.113.7", UserAgent: "test",
	}}
	return id
}

func (m *memStore) IdentityOf(_ context.Context, tenant, member uuid.UUID) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.identities[[2]uuid.UUID{tenant, member}]
	if !ok {
		return uuid.Nil, identity.ErrNotFound
	}
	return id, nil
}

func (m *memStore) List(_ context.Context, identityID uuid.UUID, now time.Time) ([]identity.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []identity.Session{}
	for _, s := range m.sessions {
		if s.IdentityID == identityID && !s.revoked && s.ExpiresAt.After(now) {
			out = append(out, s.Session)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

func (m *memStore) Revoke(_ context.Context, identityID, sessionID uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.IdentityID != identityID || s.revoked {
		return identity.ErrNotFound
	}
	s.revoked = true
	return nil
}

func (m *memStore) RevokeOthers(_ context.Context, identityID, keep uuid.UUID, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if s.IdentityID == identityID && id != keep && !s.revoked && s.ExpiresAt.After(now) {
			s.revoked = true
			n++
		}
	}
	return n, nil
}
