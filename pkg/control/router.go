package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

// dedicatedPoolIdleTTL: idle eviction for Router's dedicated/schema pool caches — a dedicated
// tenant's pool that hasn't been used in this long is torn down rather than held open forever,
// since most tenants won't be active at any given moment and each cached pool holds real
// Postgres connections.
const dedicatedPoolIdleTTL = 10 * time.Minute

type RouterErrorKind int

const (
	// ErrTenantSuspended maps to 403 — an admin suspended the tenant (e.g. non-payment).
	ErrTenantSuspended RouterErrorKind = iota
	// ErrTenantMigrating maps to 503 (retry-able) — a data-plane migration is in flight.
	ErrTenantMigrating
	// ErrTenantProvisioning maps to 503 (retry-able) — provisioning hasn't finished yet.
	ErrTenantProvisioning
	// ErrTenantExpired maps to 403 — trial expired and was never promoted to paid.
	ErrTenantExpired
	// ErrTenantDeleted maps to 404 — an admin deprovisioned the tenant; there is no resume path
	// back from this.
	ErrTenantDeleted
	// ErrInvalidSchemaName: a control.tenants row has a schema_name that doesn't pass the
	// whitelist in ValidateSchemaName — should never happen in practice, re-validated on every
	// read as defense-in-depth.
	ErrInvalidSchemaName
)

type RouterError struct {
	Kind RouterErrorKind
	// SchemaName is set only for ErrInvalidSchemaName.
	SchemaName string
}

func (e *RouterError) Error() string {
	switch e.Kind {
	case ErrTenantSuspended:
		return "tenant is suspended"
	case ErrTenantMigrating:
		return "tenant is mid-migration, retry shortly"
	case ErrTenantProvisioning:
		return "tenant is still provisioning, retry shortly"
	case ErrTenantExpired:
		return "tenant's trial has expired"
	case ErrTenantDeleted:
		return "tenant has been deleted"
	default:
		return fmt.Sprintf("invalid tenant schema name: %q", e.SchemaName)
	}
}

// ValidateSchemaName whitelists what may be interpolated into `SET LOCAL search_path TO {name}`
// — Postgres DDL-ish session settings don't accept bind parameters, so this is the only thing
// standing between a corrupted/malicious control.tenants row and SQL injection into every
// tenant-scoped transaction. "public" is the legacy/unregistered-tenant fallback; anything else
// must match ^t_[a-z0-9]+$.
func ValidateSchemaName(name string) error {
	if name == "public" {
		return nil
	}
	suffix, ok := strings.CutPrefix(name, "t_")
	if !ok || suffix == "" {
		return &RouterError{Kind: ErrInvalidSchemaName, SchemaName: name}
	}
	for _, b := range []byte(suffix) {
		if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')) {
			return &RouterError{Kind: ErrInvalidSchemaName, SchemaName: name}
		}
	}
	return nil
}

type Router struct {
	sharedPool     *pgxpool.Pool
	registry       *RegistryCache
	secretStore    SecretStore
	dedicatedPools *gocache.Cache
	schemaPools    *gocache.Cache
	// poolOpenGroup de-dupes concurrent pool-opens for the same key (dedicated or schema) — the
	// Go equivalent of moka::future::Cache::try_get_with's own de-dup guarantee for this cache.
	poolOpenGroup singleflight.Group
}

func NewRouter(sharedPool *pgxpool.Pool, registry *RegistryCache, secretStore SecretStore) *Router {
	return &Router{
		sharedPool:     sharedPool,
		registry:       registry,
		secretStore:    secretStore,
		dedicatedPools: gocache.New(dedicatedPoolIdleTTL, dedicatedPoolIdleTTL),
		schemaPools:    gocache.New(dedicatedPoolIdleTTL, dedicatedPoolIdleTTL),
	}
}

// Secrets is the SecretStore this router resolves tenant credentials through. Exposed so callers
// outside this package reach the *same* configured backend rather than building a second one
// from env.
func (r *Router) Secrets() SecretStore { return r.secretStore }

// dedicatedPool looks up (or opens and caches) the dedicated pool for a DedicatedDb tenant, keyed
// by dsnSecretRef rather than tenant id — keying on the thing that actually determines the
// physical connection is correct regardless of whether two tenants could share one by operator
// error.
func (r *Router) dedicatedPool(ctx context.Context, dsnSecretRef string) (*pgxpool.Pool, error) {
	if v, ok := r.dedicatedPools.Get(dsnSecretRef); ok {
		return v.(*pgxpool.Pool), nil
	}
	v, err, _ := r.poolOpenGroup.Do("dedicated:"+dsnSecretRef, func() (any, error) {
		if v, ok := r.dedicatedPools.Get(dsnSecretRef); ok {
			return v.(*pgxpool.Pool), nil
		}
		creds, err := r.secretStore.DbCredentials(ctx, dsnSecretRef)
		if err != nil {
			return nil, err
		}
		pool, err := pgxpool.New(ctx, creds.DSN)
		if err != nil {
			return nil, err
		}
		r.dedicatedPools.SetDefault(dsnSecretRef, pool)
		return pool, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open dedicated pool for %s: %w", dsnSecretRef, err)
	}
	return v.(*pgxpool.Pool), nil
}

// schemaPool looks up (or opens and caches) a pool whose every connection has search_path set to
// schemaName — the pool-level equivalent of what Begin does per transaction with SET LOCAL, for
// callers that genuinely can't use a transaction (DDL).
//
// "public" short-circuits to the shared pool untouched. A session-level SET search_path is
// correct here, even though Begin's own session-leak warning applies to the shared pool — the
// difference is the pool: every connection in *this* pool only ever serves this one schema.
func (r *Router) schemaPool(ctx context.Context, schemaName string) (*pgxpool.Pool, error) {
	if schemaName == "public" {
		return r.sharedPool, nil
	}
	if err := ValidateSchemaName(schemaName); err != nil {
		return nil, err
	}
	if v, ok := r.schemaPools.Get(schemaName); ok {
		return v.(*pgxpool.Pool), nil
	}

	v, err, _ := r.poolOpenGroup.Do("schema:"+schemaName, func() (any, error) {
		if v, ok := r.schemaPools.Get(schemaName); ok {
			return v.(*pgxpool.Pool), nil
		}
		cfg := r.sharedPool.Config().Copy()
		searchPathSQL := fmt.Sprintf(`SET search_path TO %q, metadata, control`, schemaName)
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, searchPathSQL)
			return err
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, err
		}
		r.schemaPools.SetDefault(schemaName, pool)
		return pool, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open schema pool for %s: %w", schemaName, err)
	}
	return v.(*pgxpool.Pool), nil
}

// resolve is the shared status/strategy resolution for Begin/PoolFor — a tenant with no
// control.tenants row is treated as {Active, Schema("public")} — the pre-Router behavior (single
// shared public schema, isolation via the tenant_id column). This is a deliberate transitional
// compatibility shim, not the intended end state: a new caller should provision a real tenant
// rather than leaning on this fallback.
func (r *Router) resolve(ctx context.Context, tenantID uuid.UUID) (TenantStrategy, error) {
	routing, err := r.registry.Get(ctx, tenantID)
	if err != nil {
		return TenantStrategy{}, err
	}
	if routing == nil {
		routing = &TenantRouting{Status: StatusActive, Strategy: SchemaStrategy("public")}
	}

	switch routing.Status {
	case StatusActive:
	case StatusSuspended:
		return TenantStrategy{}, &RouterError{Kind: ErrTenantSuspended}
	case StatusMigrating:
		return TenantStrategy{}, &RouterError{Kind: ErrTenantMigrating}
	case StatusProvisioning:
		return TenantStrategy{}, &RouterError{Kind: ErrTenantProvisioning}
	case StatusExpired:
		return TenantStrategy{}, &RouterError{Kind: ErrTenantExpired}
	case StatusDeleted:
		return TenantStrategy{}, &RouterError{Kind: ErrTenantDeleted}
	}
	return routing.Strategy, nil
}

// Begin opens a transaction scoped to tenantID — every query run against the returned
// transaction sees that tenant's schema. Callers must Commit/Rollback it themselves; this never
// commits on their behalf.
func (r *Router) Begin(ctx context.Context, tenantID uuid.UUID) (pgx.Tx, error) {
	strategy, err := r.resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	switch strategy.Kind {
	case StrategySchema:
		if err := ValidateSchemaName(strategy.SchemaName); err != nil {
			return nil, err
		}
		tx, err := r.sharedPool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		// SET LOCAL = transaction-scoped, cleans up on commit/rollback automatically — must
		// never be session-level SET, which would leak this tenant's schema to whichever
		// request the pool hands this physical connection to next.
		if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+strategy.SchemaName+", metadata, control"); err != nil {
			tx.Rollback(ctx)
			return nil, err
		}
		return tx, nil
	default:
		pool, err := r.dedicatedPool(ctx, strategy.DsnSecretRef)
		if err != nil {
			return nil, err
		}
		return pool.Begin(ctx)
	}
}

// PoolFor resolves tenantID's actual pool — the pool-level counterpart to Begin, for a caller
// that needs to run non-transactional statements (e.g. CREATE INDEX CONCURRENTLY, which can't
// run inside Begin's transaction) against the right tenant's data.
//
// Schema strategy goes through schemaPool, so the returned pool's search_path actually points at
// that tenant's schema — not the shared pool unconditionally the way an earlier version of this
// method did (a real bug: a t_<uuid> tenant was served the shared pool, whose default
// search_path resolves public, metadata, control, so a caller going through PoolFor read/wrote a
// different physical schema than Begin did for the same tenant).
func (r *Router) PoolFor(ctx context.Context, tenantID uuid.UUID) (*pgxpool.Pool, error) {
	strategy, err := r.resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	switch strategy.Kind {
	case StrategySchema:
		return r.schemaPool(ctx, strategy.SchemaName)
	default:
		return r.dedicatedPool(ctx, strategy.DsnSecretRef)
	}
}
