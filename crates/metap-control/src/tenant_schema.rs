//! Real per-tenant schema isolation for `TenantStrategy::Schema` tenants
//! (`../metap-docs/docs/features/35-per-tenant-schema-isolation.md`). `Router::begin`'s `SET
//! LOCAL search_path TO {schema_name}, metadata, control` already fully supports a genuine
//! per-tenant `schema_name` — this module is the write side that was missing: creating the
//! physical schema and cloning every table a tenant-scoped transaction can reach into it, so an
//! unqualified query inside that transaction actually finds something. Without this, a tenant
//! whose `schema_name` isn't `"public"` has no `records`/`policies`/`users`/... in its own search
//! path at all (`public` isn't in it), so nothing would resolve — this isn't purely isolation
//! polish, it's a functional prerequisite for a non-`"public"` `Schema` tenant to work at all.

use sqlx::PgPool;

use crate::router::validate_schema_name;

/// `(source_schema, table_name)`, in FK-safe creation order — every table this codebase has with
/// a `tenant_id` column, found by querying `information_schema.columns` directly against a fully
/// migrated database rather than grepping migration history (which mixes `CREATE TABLE`s with
/// later `ALTER TABLE ADD COLUMN`s across many files). Deliberately a **hardcoded, reviewed
/// list**, not derived from `information_schema` at provisioning time — a table gaining a
/// `tenant_id` column later should force a visible decision here (belongs in this list, or is
/// deliberately excluded like `control.tenant_hostnames`/`metadata.outbox_events` below), not
/// silently grow or shrink this list unreviewed.
///
/// Excluded, both deliberately: `control.tenant_hostnames` has a `tenant_id` column but is
/// itself genuinely global platform config (which hostname maps to which tenant) — same category
/// as `control.tenants` itself, never copied per-tenant. `metadata.outbox_events` has no
/// `tenant_id` column at all — one shared table, drained by one `outbox-publisher` regardless of
/// tenant, correct as-is.
const TENANT_SCOPED_TABLES: &[(&str, &str)] = &[
    ("metadata", "dashboard_configs"),
    ("metadata", "policies"),
    ("metadata", "reconciler_backfill_progress"),
    ("metadata", "reconciler_entity_deployments"),
    ("metadata", "reconciler_entity_status"),
    ("metadata", "tenant_auth_configs"),
    ("metadata", "tenant_configs"),
    ("metadata", "user_preferences"),
    ("metadata", "user_roles"),
    ("metadata", "users"),
    ("metadata", "workflow_events"),
    ("public", "attachments"),
    ("public", "records"),
    // These 3 have FKs to each other (below) — created last, in dependency order, so
    // `create_tenant_schema` can add the FK constraints in one pass right after this loop
    // without needing a second topological sort at runtime.
    ("metadata", "cron_jobs"),
    ("metadata", "cron_job_runs"),
    ("metadata", "workflow_runs"),
];

/// `(table, column, ref_table, ref_column)` — every FK among the tables above, found via
/// `information_schema.table_constraints`/`key_column_usage`/`constraint_column_usage`.
/// Postgres's `CREATE TABLE (LIKE ...)` never copies foreign keys under any `INCLUDING` option
/// (a real Postgres limitation, not an oversight in `create_tenant_schema` below) — these are
/// added back by hand, once per tenant schema, after every table in [`TENANT_SCOPED_TABLES`]
/// exists. All 3 are `ON DELETE CASCADE` on the source tables (confirmed via `\d`) — matched
/// here, not just "a foreign key of some kind", since a mismatched delete rule would be a real,
/// silent behavior difference between a tenant's cloned table and the original.
const TENANT_SCOPED_FOREIGN_KEYS: &[(&str, &str, &str, &str)] = &[
    ("cron_job_runs", "job_id", "cron_jobs", "id"),
    ("workflow_runs", "cron_job_run_id", "cron_job_runs", "id"),
    ("workflow_runs", "job_id", "cron_jobs", "id"),
];

/// `CREATE SCHEMA {schema_name}`, then clones every table in [`TENANT_SCOPED_TABLES`] into it
/// (`CREATE TABLE ... (LIKE source INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES)`
/// — confirmed live that none of these tables use `SERIAL`/`IDENTITY`, every primary key is
/// `uuid DEFAULT gen_random_uuid()`, so `INCLUDING DEFAULTS` alone handles PK generation
/// correctly with no sequence-ownership edge case to worry about), then adds the 3
/// [`TENANT_SCOPED_FOREIGN_KEYS`] by hand. Called once, before a tenant's `control.tenants` row
/// is written (mirrors `provisioning::provision_dedicated_db_tenant`'s own ordering: finish every
/// piece of setup DDL first, only then make the tenant visible to `Router`).
///
/// **Idempotent by construction** (`IF NOT EXISTS` throughout, `ADD CONSTRAINT` tolerates
/// `42710`/`duplicate_object` the same way `metap-reconciler::executor::ensure_schema_exists`
/// already does for its own schema-creation race) — not for concurrency (provisioning one tenant
/// isn't a hot path worth racing), but so a second `provision_schema_tenant` call for an
/// **already-provisioned** `tenant_id` fails at the `control.tenants` INSERT it still runs after
/// this, exactly as before this feature existed, instead of failing here with a generic
/// "schema/table already exists" error that `metap-control-http`'s `duplicate_tenant_id_response`
/// (downcasts to a `unique_violation` for a clean `409`) wouldn't recognize.
pub async fn create_tenant_schema(pool: &PgPool, schema_name: &str) -> anyhow::Result<()> {
    validate_schema_name(schema_name).map_err(|e| anyhow::anyhow!("{e}"))?;

    sqlx::query(&format!("CREATE SCHEMA IF NOT EXISTS \"{schema_name}\""))
        .execute(pool)
        .await?;

    for (source_schema, table) in TENANT_SCOPED_TABLES {
        sqlx::query(&format!(
            "CREATE TABLE IF NOT EXISTS \"{schema_name}\".\"{table}\" (LIKE \"{source_schema}\".\"{table}\" \
             INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES)"
        ))
        .execute(pool)
        .await?;
    }

    for (table, column, ref_table, ref_column) in TENANT_SCOPED_FOREIGN_KEYS {
        let result = sqlx::query(&format!(
            "ALTER TABLE \"{schema_name}\".\"{table}\" ADD CONSTRAINT \"{table}_{column}_fkey\" \
             FOREIGN KEY (\"{column}\") REFERENCES \"{schema_name}\".\"{ref_table}\"(\"{ref_column}\") \
             ON DELETE CASCADE"
        ))
        .execute(pool)
        .await;
        match result {
            Ok(_) => {}
            Err(sqlx::Error::Database(db_err)) if db_err.code().as_deref() == Some("42710") => {}
            Err(e) => return Err(e.into()),
        }
    }

    Ok(())
}
