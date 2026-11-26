package settings

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/config"
)

// PlatformEntry is a key a platform admin may write, with its fleet-wide value.
type PlatformEntry struct {
	Def   *config.KeyDef
	Value json.RawMessage
}

// TenantEntry is a Tenant-tier key with the value one tenant sees, and whether it set that itself.
type TenantEntry struct {
	Def        *config.KeyDef
	Value      json.RawMessage
	Overridden bool
}

// Store is the tiered configuration as the slice needs it (package config holds the registry, the
// validation and the snapshots; PgStore adapts it). Errors are *config.Error, which mapErr turns into
// the platform envelope.
type Store interface {
	// PlatformView is every key a platform admin may write with its fleet-wide value.
	PlatformView() []PlatformEntry
	// PlatformValue is a key's fleet-wide value (nil for a key the registry does not know).
	PlatformValue(key string) json.RawMessage
	// SetPlatform and ResetPlatform write the fleet-wide tier, on the shared database.
	SetPlatform(ctx context.Context, key string, value json.RawMessage) error
	ResetPlatform(ctx context.Context, key string) error

	// TenantView is every Tenant-tier key with the value the tenant sees.
	TenantView(ctx context.Context, tenant uuid.UUID) []TenantEntry
	// TenantValue is the value the tenant sees for a key (nil for a key the registry does not know).
	TenantValue(ctx context.Context, tenant uuid.UUID, key string) json.RawMessage
	// SetTenant and ResetTenant write the tenant's own override, on the tenant's database.
	SetTenant(ctx context.Context, tenant uuid.UUID, key string, value json.RawMessage) error
	ResetTenant(ctx context.Context, tenant uuid.UUID, key string) error
	// DefaultFor is what a key reads back once the tenant's override is gone: the fleet default a
	// platform admin set, or the declared one.
	DefaultFor(key string) json.RawMessage
}
