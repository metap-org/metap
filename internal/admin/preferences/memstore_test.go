package preferences

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// memStore is the Store the unit tests use.
type memStore struct {
	mu      sync.Mutex
	locales map[[2]uuid.UUID]string
}

func newMemStore() *memStore { return &memStore{locales: map[[2]uuid.UUID]string{}} }

func (m *memStore) Locale(_ context.Context, tenant, user uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locales[[2]uuid.UUID{tenant, user}]; ok {
		return l, nil
	}
	return DefaultLocale, nil
}

func (m *memStore) SetLocale(_ context.Context, tenant, user uuid.UUID, locale string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locales[[2]uuid.UUID{tenant, user}] = locale
	return nil
}
