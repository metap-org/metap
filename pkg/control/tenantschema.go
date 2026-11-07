// Real per-tenant schema isolation for Schema-strategy tenants. Router.Begin's
// `SET LOCAL search_path TO {schema_name}, metadata, control` already fully supports a genuine
// per-tenant schema_name — this file is the write side: creating the physical schema and
// cloning the per-tenant tables into it.
package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantScopedTables: (source_schema, table_name), in FK-safe creation order — every table in
// this codebase with a tenant_id column. Deliberately a hardcoded, reviewed list, not derived
// from information_schema at provisioning time — a table gaining a tenant_id column later
// should force a visible decision here.
//
// Excluded, all deliberately: control.tenant_hostnames (genuinely global platform config, never
// copied per-tenant), metadata.outbox_events (no tenant_id column, one shared table drained
// regardless of tenant), metadata.records (no longer exists — the shared generic table every
// entity used before table-per-entity).
//
// metadata.users/metadata.user_roles are excluded too, and this one is load-bearing: identity in
// this platform is deliberately global, not per-tenant. POST /auth/login resolves which tenant a
// caller belongs to from the user row it finds, with no tenant predicate, because there is no
// tenant picker in the UI to supply one — which is exactly why users_email_unique is global
// rather than (tenant_id, email). Cloning users broke both halves of that at once in an earlier
// version of this feature: a new tenant's admin became unreachable from the tenant-less login
// path, and INCLUDING INDEXES turned the deliberately-global unique index into a per-schema one.
// Leaving them shared costs nothing functionally — Router.Begin puts metadata in every tenant
// transaction's search_path, so an unqualified users/user_roles query still resolves to the
// shared table, still filtered by each row's own tenant_id column.
var tenantScopedTables = [][2]string{
	{"metadata", "dashboard_configs"},
	{"metadata", "policies"},
	{"metadata", "reconciler_backfill_progress"},
	{"metadata", "reconciler_entity_deployments"},
	{"metadata", "reconciler_entity_status"},
	{"metadata", "tenant_auth_configs"},
	{"metadata", "tenant_configs"},
	{"metadata", "user_preferences"},
	{"metadata", "workflow_events"},
	{"metadata", "attachments"},
	// These 3 have FKs to each other (below) — created last, in dependency order.
	{"metadata", "cron_jobs"},
	{"metadata", "cron_job_runs"},
	{"metadata", "workflow_runs"},
}

// tenantScopedForeignKeys: (table, column, refTable, refColumn) — every FK among the tables
// above. Postgres's CREATE TABLE (LIKE ...) never copies foreign keys under any INCLUDING
// option, so these are added back by hand, once per tenant schema, after every table above
// exists. All 3 are ON DELETE CASCADE on the source tables, matched here exactly.
var tenantScopedForeignKeys = [][4]string{
	{"cron_job_runs", "job_id", "cron_jobs", "id"},
	{"workflow_runs", "cron_job_run_id", "cron_job_runs", "id"},
	{"workflow_runs", "job_id", "cron_jobs", "id"},
}

// CreateTenantSchema: CREATE SCHEMA {schemaName}, then clones every table in
// tenantScopedTables into it, then adds the 3 tenantScopedForeignKeys by hand. Called once,
// before a tenant's control.tenants row is written.
//
// Idempotent by construction (IF NOT EXISTS throughout, ADD CONSTRAINT tolerates
// duplicate_object/42710) — not for concurrency, but so a second call for an
// already-provisioned tenantID fails at the control.tenants INSERT it still runs after this,
// instead of failing here with a generic "already exists" error the caller wouldn't recognize
// as a clean 409.
func CreateTenantSchema(ctx context.Context, pool *pgxpool.Pool, schemaName string) error {
	if err := ValidateSchemaName(schemaName); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schemaName)); err != nil {
		return err
	}

	for _, t := range tenantScopedTables {
		sourceSchema, table := t[0], t[1]
		sql := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %q.%q (LIKE %q.%q INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES)`,
			schemaName, table, sourceSchema, table)
		if _, err := pool.Exec(ctx, sql); err != nil {
			return err
		}
	}

	for _, fk := range tenantScopedForeignKeys {
		table, column, refTable, refColumn := fk[0], fk[1], fk[2], fk[3]
		sql := fmt.Sprintf(`ALTER TABLE %q.%q ADD CONSTRAINT %q FOREIGN KEY (%q) REFERENCES %q.%q(%q) ON DELETE CASCADE`,
			schemaName, table, table+"_"+column+"_fkey", column, schemaName, refTable, refColumn)
		if _, err := pool.Exec(ctx, sql); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42710" {
				continue
			}
			return err
		}
	}

	return nil
}
