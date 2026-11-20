package control

import (
	"context"
	"sync"
)

// MemorySecretStore keeps general secrets in process memory and reads everything else (database
// credentials, and secrets it was not given) from the environment like EnvStore. For tests and
// single-process development: a tenant can manage its own secrets without Vault/AWS/GCP, and
// nothing survives a restart.
type MemorySecretStore struct {
	EnvStore
	mu sync.Mutex
	m  map[string]string
}

func NewMemorySecretStore() *MemorySecretStore { return &MemorySecretStore{m: map[string]string{}} }

func (s *MemorySecretStore) GetSecret(ctx context.Context, ref string) (string, error) {
	s.mu.Lock()
	v, ok := s.m[ref]
	s.mu.Unlock()
	if ok {
		return v, nil
	}
	return s.EnvStore.GetSecret(ctx, ref)
}

func (s *MemorySecretStore) PutSecret(_ context.Context, ref, value string) error {
	s.mu.Lock()
	s.m[ref] = value
	s.mu.Unlock()
	return nil
}

func (s *MemorySecretStore) DeleteSecret(_ context.Context, ref string) error {
	s.mu.Lock()
	delete(s.m, ref)
	s.mu.Unlock()
	return nil
}
