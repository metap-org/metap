package settings

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/tenancy"
)

// EffectiveConfigFunc is the cache-first read of a tenant's effective configuration.
type EffectiveConfigFunc func(ctx context.Context, tenant uuid.UUID) *config.EffectiveConfig

// PgStore adapts config.Store: the platform tier on the shared database, a tenant's overrides on its
// own.
type PgStore struct {
	store     *config.Store
	router    *tenancy.Router
	effective EffectiveConfigFunc
}

func NewPgStore(store *config.Store, router *tenancy.Router, effective EffectiveConfigFunc) *PgStore {
	return &PgStore{store: store, router: router, effective: effective}
}

func (s *PgStore) PlatformView() []PlatformEntry {
	view := s.store.Current().PlatformWritableView()
	out := make([]PlatformEntry, len(view))
	for i, e := range view {
		out[i] = PlatformEntry{Def: e.Def, Value: e.Value}
	}
	return out
}

// A key the registry does not know has no value (Get panics on it): the change is refused for that
// reason a moment later, and a refused change is not audited.
func (s *PgStore) PlatformValue(key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return s.store.Current().Get(key)
}

func (s *PgStore) SetPlatform(ctx context.Context, key string, value json.RawMessage) error {
	return s.store.SetPlatformGlobalIn(ctx, kernel.IdentityDB{Pool: s.router.SharedPool()}, key, value)
}

func (s *PgStore) ResetPlatform(ctx context.Context, key string) error {
	return s.store.ResetPlatformGlobalIn(ctx, kernel.IdentityDB{Pool: s.router.SharedPool()}, key)
}

func (s *PgStore) TenantView(ctx context.Context, tenant uuid.UUID) []TenantEntry {
	view := s.effective(ctx, tenant).TenantView()
	out := make([]TenantEntry, len(view))
	for i, e := range view {
		out[i] = TenantEntry{Def: e.Def, Value: e.Value, Overridden: e.Overridden}
	}
	return out
}

func (s *PgStore) TenantValue(ctx context.Context, tenant uuid.UUID, key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return s.effective(ctx, tenant).Get(key)
}

func (s *PgStore) SetTenant(ctx context.Context, tenant uuid.UUID, key string, value json.RawMessage) error {
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return err
	}
	if err := s.store.SetTenant(ctx, tx, tenant, key, value); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) ResetTenant(ctx context.Context, tenant uuid.UUID, key string) error {
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return err
	}
	if err := s.store.ResetTenant(ctx, tx, tenant, key); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// DefaultFor reads the platform snapshot: the tenant cache drops its copy only when the change
// commits, so asking it here would still show the override.
func (s *PgStore) DefaultFor(key string) json.RawMessage { return s.store.Current().Get(key) }
