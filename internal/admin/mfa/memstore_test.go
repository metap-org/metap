package mfa

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// goodCode is the one code the in-memory factor accepts (the real one is time based).
const goodCode = "424242"

// memStore is the Store the unit tests use: a factor that accepts goodCode, with the same states and
// errors as the real one, and that counts wrong codes the same way (identity.MaxMFAFailures).
type memStore struct {
	mu         sync.Mutex
	identities map[[2]uuid.UUID]uuid.UUID
	factors    map[uuid.UUID]*memFactor
}

type memFactor struct {
	pending, on bool
	codes       int
	failures    int
	locked      bool
}

func newMemStore() *memStore {
	return &memStore{identities: map[[2]uuid.UUID]uuid.UUID{}, factors: map[uuid.UUID]*memFactor{}}
}

func (m *memStore) person(tenant uuid.UUID) (member uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	member = uuid.New()
	m.identities[[2]uuid.UUID{tenant, member}] = uuid.New()
	return member
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

func (m *memStore) Email(context.Context, uuid.UUID) (string, error) {
	return "someone@example.com", nil
}

func (m *memStore) factor(id uuid.UUID) *memFactor {
	f := m.factors[id]
	if f == nil {
		f = &memFactor{}
		m.factors[id] = f
	}
	return f
}

func (m *memStore) Status(_ context.Context, id uuid.UUID) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.factor(id)
	if !f.on {
		return false, 0, nil
	}
	return true, f.codes, nil
}

func (m *memStore) Begin(_ context.Context, id uuid.UUID, _ string, _ time.Time) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.factor(id)
	if f.on {
		return "", "", identity.ErrMFAAlreadyOn
	}
	f.pending = true
	return "MEMSECRET", "otpauth://totp/memory?secret=MEMSECRET", nil
}

// check is VerifyMFA's counting, for the factor that is on.
func (f *memFactor) check(code string) error {
	if !f.on {
		return identity.ErrMFANotEnrolled
	}
	if f.locked {
		return identity.ErrMFALocked
	}
	if code == goodCode {
		f.failures = 0
		return nil
	}
	f.failures++
	if f.failures >= identity.MaxMFAFailures {
		f.failures, f.locked = 0, true
	}
	return identity.ErrMFAInvalidCode
}

func (m *memStore) Confirm(_ context.Context, id uuid.UUID, code string, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.factor(id)
	switch {
	case f.on:
		return nil, identity.ErrMFAAlreadyOn
	case !f.pending:
		return nil, identity.ErrMFAEnrollNotRun
	case code != goodCode:
		return nil, identity.ErrMFAInvalidCode
	}
	f.on, f.pending, f.codes = true, false, 10
	return recoveryCodes(10), nil
}

func (m *memStore) Disable(_ context.Context, id uuid.UUID, code string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.factor(id)
	if err := f.check(code); err != nil {
		return err
	}
	*f = memFactor{}
	return nil
}

func (m *memStore) Regenerate(_ context.Context, id uuid.UUID, code string, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.factor(id)
	if err := f.check(code); err != nil {
		return nil, err
	}
	f.codes = 10
	return recoveryCodes(10), nil
}

func recoveryCodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = uuid.NewString()
	}
	return out
}
