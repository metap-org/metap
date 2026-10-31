// Package config holds platform configuration stored in Postgres instead of hard-coded in Go or
// pinned to an env var at boot — ported from metap's Rust metap-config crate.
//
// Not a free-form key/value bag. Every settable key is declared in Registry with a tier, a
// default and a validator; an unknown key is rejected rather than stored.
//
// Tiering is the security boundary, not a convenience: an Operator key is readable by nobody's
// API and writable by nobody's API.
//
// Three tiers, resolved in one direction: declared default <- platform_configs <- tenant_configs,
// each tier overriding only the keys it actually stores a row for (EffectiveConfig).
//
// Reads never touch the database: ConfigStore loads the table once at boot into an atomic
// snapshot and swaps a fresh one in after every write.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gocache "github.com/patrickmn/go-cache"
)

// Snapshot is an immutable view of every stored platform-tier override. Values absent here fall
// back to the key's declared default.
type Snapshot struct {
	overrides map[string]json.RawMessage
}

func newSnapshot(overrides map[string]json.RawMessage) *Snapshot {
	if overrides == nil {
		overrides = map[string]json.RawMessage{}
	}
	return &Snapshot{overrides: overrides}
}

// Get returns the effective value for key: the stored override if there is one, else the
// declared default. Panics for a key that isn't in the registry at all — that is a programming
// error (a typo'd constant), not a runtime condition.
func (s *Snapshot) Get(key string) json.RawMessage {
	if v, ok := s.overrides[key]; ok {
		return v
	}
	def := Lookup(key)
	if def == nil {
		panic(fmt.Sprintf("config key %q is not in the registry", key))
	}
	return def.Default()
}

// GetU64 is convenience for the numeric keys. Falls back to the declared default if a stored
// value somehow isn't a number.
func (s *Snapshot) GetU64(key string) uint64 {
	v := s.Get(key)
	var n float64
	if err := json.Unmarshal(v, &n); err == nil && n >= 0 {
		return uint64(n)
	}
	slog.Warn("stored config value is not a positive integer; using default", "key", key, "value", string(v))
	def := Lookup(key)
	var defN float64
	_ = json.Unmarshal(def.Default(), &defN)
	return uint64(defN)
}

// PlatformWritableView is every key a platform admin may write, with its current fleet-wide
// value. Operator keys are excluded on purpose.
func (s *Snapshot) PlatformWritableView() []struct {
	Def   *KeyDef
	Value json.RawMessage
} {
	var out []struct {
		Def   *KeyDef
		Value json.RawMessage
	}
	for i := range Registry {
		def := &Registry[i]
		if def.Level == LevelPlatformGlobal || def.Level == LevelTenant {
			out = append(out, struct {
				Def   *KeyDef
				Value json.RawMessage
			}{def, s.Get(def.Key)})
		}
	}
	return out
}

// TenantSnapshot holds only keys one tenant explicitly set.
type TenantSnapshot struct {
	overrides map[string]json.RawMessage
}

// EffectiveConfig is a tenant's effective configuration: their own overrides layered over the
// fleet snapshot.
type EffectiveConfig struct {
	platform *Snapshot
	tenant   *TenantSnapshot // nil for a caller with no tenant at all
}

func NewEffectiveConfig(platform *Snapshot, tenant *TenantSnapshot) *EffectiveConfig {
	return &EffectiveConfig{platform: platform, tenant: tenant}
}

// Get returns the tenant override, else the fleet default, else the value declared in Go.
func (e *EffectiveConfig) Get(key string) json.RawMessage {
	if e.tenant != nil {
		if v, ok := e.tenant.overrides[key]; ok {
			return v
		}
	}
	return e.platform.Get(key)
}

func (e *EffectiveConfig) GetU64(key string) uint64 {
	v := e.Get(key)
	var n float64
	if err := json.Unmarshal(v, &n); err == nil && n >= 0 {
		return uint64(n)
	}
	slog.Warn("stored config value is not a positive integer; using default", "key", key, "value", string(v))
	def := Lookup(key)
	var defN float64
	_ = json.Unmarshal(def.Default(), &defN)
	return uint64(defN)
}

// TenantView is every Tenant key with its effective value, plus whether this tenant set it.
func (e *EffectiveConfig) TenantView() []struct {
	Def        *KeyDef
	Value      json.RawMessage
	Overridden bool
} {
	var out []struct {
		Def        *KeyDef
		Value      json.RawMessage
		Overridden bool
	}
	for i := range Registry {
		def := &Registry[i]
		if def.Level != LevelTenant {
			continue
		}
		overridden := false
		if e.tenant != nil {
			_, overridden = e.tenant.overrides[def.Key]
		}
		out = append(out, struct {
			Def        *KeyDef
			Value      json.RawMessage
			Overridden bool
		}{def, e.Get(def.Key), overridden})
	}
	return out
}

// SecretRef returns the stored reference for a secret key, or ("", false) when this tenant has
// no credential. There is no counterpart returning a value — the plaintext is not in this
// package's data at all.
func (e *EffectiveConfig) SecretRef(key string) (string, bool) {
	var marker struct {
		SecretRef string `json:"secretRef"`
	}
	if err := json.Unmarshal(e.Get(key), &marker); err != nil || marker.SecretRef == "" {
		return "", false
	}
	return marker.SecretRef, true
}

// PublicView is only the keys declared Public — the unauthenticated hostname-resolved surface's
// entire response.
func (e *EffectiveConfig) PublicView() []struct {
	Key   string
	Value json.RawMessage
} {
	var out []struct {
		Key   string
		Value json.RawMessage
	}
	for _, def := range Registry {
		if def.Public {
			out = append(out, struct {
				Key   string
				Value json.RawMessage
			}{def.Key, e.Get(def.Key)})
		}
	}
	return out
}

// Error is why a config write was refused.
type Error struct {
	Kind   ErrorKind
	Key    string
	Reason string
	Cause  error
}

type ErrorKind int

const (
	ErrUnknownKey ErrorKind = iota
	ErrNotWritable
	ErrInvalid
	ErrDB
)

func (e *Error) Error() string {
	switch e.Kind {
	case ErrUnknownKey:
		return fmt.Sprintf("unknown config key %q", e.Key)
	case ErrNotWritable:
		return fmt.Sprintf("config key %q is not writable here: %s", e.Key, e.Reason)
	case ErrInvalid:
		return fmt.Sprintf("invalid value for %q: %s", e.Key, e.Reason)
	default:
		return fmt.Sprintf("config storage error: %v", e.Cause)
	}
}

func (e *Error) Unwrap() error { return e.Cause }

func unknownKeyErr(key string) error { return &Error{Kind: ErrUnknownKey, Key: key} }
func notWritableErr(key, reason string) error {
	return &Error{Kind: ErrNotWritable, Key: key, Reason: reason}
}
func invalidErr(key, reason string) error { return &Error{Kind: ErrInvalid, Key: key, Reason: reason} }
func dbErr(cause error) error             { return &Error{Kind: ErrDB, Cause: cause} }

// TenantCacheTTL matches metap_control::RegistryCache's 30s for the same reason.
const TenantCacheTTL = 30 * time.Second

type executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Store loads and caches platform_configs, and is the only writer of it and of tenant_configs.
type Store struct {
	pool     *pgxpool.Pool
	snapshot atomic.Pointer[Snapshot]
	tenants  *gocache.Cache
}

// Unloaded returns a store that has not read the table yet: every key reads back its declared
// default. Exists because synchronous construction can't also be async; "no overrides" is
// exactly the pre-existing behavior, so a caller that never calls Reload keeps working precisely
// as it did.
func Unloaded(pool *pgxpool.Pool) *Store {
	s := &Store{pool: pool, tenants: gocache.New(TenantCacheTTL, TenantCacheTTL*2)}
	s.snapshot.Store(newSnapshot(nil))
	return s
}

// Reload re-reads the table into a fresh snapshot.
func (s *Store) Reload(ctx context.Context) error {
	snap, err := s.readSnapshot(ctx)
	if err != nil {
		return err
	}
	s.snapshot.Store(snap)
	return nil
}

// Load reads the whole table once and returns a populated Store.
func Load(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	s := Unloaded(pool)
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) readSnapshot(ctx context.Context) (*Snapshot, error) {
	rows, err := s.pool.Query(ctx, "SELECT key, value FROM metadata.platform_configs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value json.RawMessage
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if Lookup(key) == nil {
			slog.Warn("ignoring stored config row for a key not in the registry", "key", key)
			continue
		}
		overrides[key] = value
	}
	return newSnapshot(overrides), rows.Err()
}

func (s *Store) Current() *Snapshot {
	return s.snapshot.Load()
}

// SetPlatformGlobal writes one PlatformGlobal/Tenant key and swaps in a fresh snapshot. Refuses
// Operator keys. Tier is checked before validity so an operator-only key reports why it can't be
// written rather than leaking whether the proposed value would have been acceptable.
func (s *Store) SetPlatformGlobal(ctx context.Context, key string, value json.RawMessage) error {
	def := Lookup(key)
	if def == nil {
		return unknownKeyErr(key)
	}
	if def.Level == LevelOperator {
		return notWritableErr(key, "operator-only, settable through deployment configuration only")
	}
	if err := def.Validate(value); err != nil {
		return invalidErr(key, err.Error())
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO metadata.platform_configs (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value)
	if err != nil {
		return dbErr(err)
	}
	snap, err := s.readSnapshot(ctx)
	if err != nil {
		return dbErr(err)
	}
	s.snapshot.Store(snap)
	slog.Info("platform config updated", "key", key)
	return nil
}

// ResetPlatformGlobal removes an override so the key falls back to its declared default.
func (s *Store) ResetPlatformGlobal(ctx context.Context, key string) error {
	def := Lookup(key)
	if def == nil {
		return unknownKeyErr(key)
	}
	if def.Level == LevelOperator {
		return notWritableErr(key, "operator-only, settable through deployment configuration only")
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM metadata.platform_configs WHERE key = $1", key); err != nil {
		return dbErr(err)
	}
	snap, err := s.readSnapshot(ctx)
	if err != nil {
		return dbErr(err)
	}
	s.snapshot.Store(snap)
	return nil
}

// CachedTenant returns the cached snapshot for a tenant, or (nil, false) if it isn't cached — a
// caller can answer a read without opening a tenant transaction at all.
func (s *Store) CachedTenant(tenantID uuid.UUID) (*TenantSnapshot, bool) {
	v, ok := s.tenants.Get(tenantID.String())
	if !ok {
		return nil, false
	}
	return v.(*TenantSnapshot), true
}

// LoadTenant reads one tenant's overrides through the caller-supplied tenant-routed executor and
// caches the result.
func (s *Store) LoadTenant(ctx context.Context, e executor, tenantID uuid.UUID) (*TenantSnapshot, error) {
	rows, err := e.Query(ctx, "SELECT key, value FROM metadata.tenant_configs WHERE tenant_id = $1", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value json.RawMessage
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		def := Lookup(key)
		switch {
		case def == nil:
			slog.Warn("ignoring tenant config row for a key not in the registry", "key", key)
		case def.Level != LevelTenant:
			slog.Warn("ignoring tenant config row for a key that is no longer tenant-writable", "key", key)
		default:
			overrides[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	snap := &TenantSnapshot{overrides: overrides}
	s.tenants.Set(tenantID.String(), snap, gocache.DefaultExpiration)
	return snap, nil
}

// Effective pairs a tenant's overrides with the fleet snapshot. tenant == nil reads back the
// fleet-wide values.
func (s *Store) Effective(tenant *TenantSnapshot) *EffectiveConfig {
	return NewEffectiveConfig(s.Current(), tenant)
}

func (s *Store) tenantWritableKey(key string) (*KeyDef, error) {
	def := Lookup(key)
	if def == nil {
		return nil, unknownKeyErr(key)
	}
	switch def.Level {
	case LevelTenant:
		return def, nil
	case LevelOperator:
		return nil, notWritableErr(key, "operator-only, settable through deployment configuration only")
	default:
		return nil, notWritableErr(key, "fleet-wide, settable only by a platform administrator")
	}
}

// SetTenant writes one tenant's override for a Tenant-tier key. tenantID must come from the
// caller's verified identity, never from a request body.
func (s *Store) SetTenant(ctx context.Context, e executor, tenantID uuid.UUID, key string, value json.RawMessage) error {
	def, err := s.tenantWritableKey(key)
	if err != nil {
		return err
	}
	if def.Secret {
		return notWritableErr(key, "holds a credential; write it through the secret path, not as a config value")
	}
	if err := def.Validate(value); err != nil {
		return invalidErr(key, err.Error())
	}
	if _, err := e.Exec(ctx,
		`INSERT INTO metadata.tenant_configs (tenant_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		tenantID, key, value); err != nil {
		return dbErr(err)
	}
	s.tenants.Delete(tenantID.String())
	slog.Info("tenant config updated", "tenant_id", tenantID, "key", key)
	return nil
}

// ResetTenant clears a tenant's override so the key falls back to the fleet default, then to the
// declared one.
func (s *Store) ResetTenant(ctx context.Context, e executor, tenantID uuid.UUID, key string) error {
	if _, err := s.tenantWritableKey(key); err != nil {
		return err
	}
	if _, err := e.Exec(ctx, "DELETE FROM metadata.tenant_configs WHERE tenant_id = $1 AND key = $2", tenantID, key); err != nil {
		return dbErr(err)
	}
	s.tenants.Delete(tenantID.String())
	return nil
}

// ValidateTenantSecret validates a proposed plaintext credential for a secret key and hands back
// its declaration, without storing anything — the write belongs in a SecretStore this package
// deliberately does not depend on.
func (s *Store) ValidateTenantSecret(key string, plaintext json.RawMessage) (*KeyDef, error) {
	def, err := s.tenantWritableKey(key)
	if err != nil {
		return nil, err
	}
	if !def.Secret {
		return nil, notWritableErr(key, "is not a credential; write it as an ordinary config value")
	}
	if err := def.Validate(plaintext); err != nil {
		return nil, invalidErr(key, err.Error())
	}
	return def, nil
}

// SetTenantSecretMarker records that a tenant has a credential stored for key, as
// {"secretRef": ...}. secretRef must be server-derived — there is no untrusted string for this
// function to have to distrust.
func (s *Store) SetTenantSecretMarker(ctx context.Context, e executor, tenantID uuid.UUID, key, secretRef string) error {
	def, err := s.tenantWritableKey(key)
	if err != nil {
		return err
	}
	if !def.Secret {
		return notWritableErr(key, "is not a credential")
	}
	marker, _ := json.Marshal(map[string]string{"secretRef": secretRef})
	if _, err := e.Exec(ctx,
		`INSERT INTO metadata.tenant_configs (tenant_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		tenantID, key, marker); err != nil {
		return dbErr(err)
	}
	s.tenants.Delete(tenantID.String())
	slog.Info("tenant credential stored", "tenant_id", tenantID, "key", key)
	return nil
}

// InvalidateTenant drops a tenant's cached snapshot.
func (s *Store) InvalidateTenant(tenantID uuid.UUID) {
	s.tenants.Delete(tenantID.String())
}
