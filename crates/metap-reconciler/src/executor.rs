//! §5.6 — runs a planned `Vec<DdlOp>` against Postgres: an advisory lock so two reconciles for
//! the same `(tenant, entity)` never run concurrently, `Migrating` status while any `Cost::Heavy`
//! op is in flight (§5.5), each op dispatched by its `execution_mode()`.
//!
//! **Not this crate's job**: persisting the new `EntityDefinition` into whatever registry the
//! caller uses. §5.6's "commit_metadata GATE — chỉ .store() registry SAU khi DDL xong" is an
//! invariant for the *caller* to honor (this crate has no registry to write to) —
//! `reconcile::reconcile` only returns `Ok` after every op below has actually run; a caller must
//! not treat an entity's new field/index as live until that `Ok` comes back.

use sqlx::PgPool;
use uuid::Uuid;

use crate::backfill;
use crate::diff::DdlOp;
use crate::schema::{Cost, ExecutionMode, FkSpec, IndexSpec, PhysicalSchema, UniqueSpec};
use crate::sqlfmt::{quote_ident, quote_literal};
use crate::status;

pub async fn execute(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    desired: &PhysicalSchema,
    ops: &[DdlOp],
) -> anyhow::Result<()> {
    if ops.is_empty() {
        return Ok(());
    }

    // A dedicated connection held for the lock's entire lifetime — see `status::try_advisory_lock`'s
    // doc comment for why this must not be a bare `&PgPool` call. The actual DDL/backfill work
    // below still goes through `pool` freely; only the lock itself needs this one pinned connection.
    let mut lock_conn = pool.acquire().await?;
    let lock_owner = Uuid::new_v4();
    if !status::try_advisory_lock(&mut lock_conn, tenant_id, entity_name).await? {
        anyhow::bail!("another reconcile is already running for {entity_name}");
    }
    let result = execute_locked(pool, tenant_id, entity_name, desired, ops, lock_owner).await;
    status::advisory_unlock(&mut lock_conn, tenant_id, entity_name).await?;
    result
}

async fn execute_locked(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    desired: &PhysicalSchema,
    ops: &[DdlOp],
    lock_owner: Uuid,
) -> anyhow::Result<()> {
    let has_heavy = ops.iter().any(|op| op.cost() == Cost::Heavy);
    if has_heavy {
        status::set_status(
            pool,
            tenant_id,
            entity_name,
            status::EntityStatus::Migrating,
            lock_owner,
            chrono::Duration::minutes(10),
        )
        .await?;
    }

    for op in ops {
        let outcome = run_one(pool, tenant_id, entity_name, desired, op).await;
        if let Err(err) = outcome {
            status::record_error(pool, tenant_id, entity_name, &format!("{err:#}")).await?;
            return Err(err);
        }
    }

    if has_heavy {
        status::set_status(
            pool,
            tenant_id,
            entity_name,
            status::EntityStatus::Active,
            lock_owner,
            chrono::Duration::zero(),
        )
        .await?;
    }
    Ok(())
}

async fn run_one(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    desired: &PhysicalSchema,
    op: &DdlOp,
) -> anyhow::Result<()> {
    match op.execution_mode() {
        ExecutionMode::Transactional => {
            let mut tx = pool.begin().await?;
            for stmt in build_sql(&desired.table, op) {
                sqlx::query(&stmt).execute(&mut *tx).await?;
            }
            tx.commit().await?;
            Ok(())
        }
        ExecutionMode::NonTransactional => {
            // `CREATE`/`DROP INDEX CONCURRENTLY` errors inside a transaction block — a plain
            // `PgPool::execute` does not wrap in one (unlike `pool.begin()`), matching
            // `crates/metap-peripherals/src/index_reconciler.rs`'s existing convention.
            for stmt in build_sql(&desired.table, op) {
                sqlx::query(&stmt).execute(pool).await?;
            }
            Ok(())
        }
        ExecutionMode::Batched => {
            let DdlOp::BackfillColumn {
                op_id,
                column,
                source_field,
                sql_type,
            } = op
            else {
                unreachable!("only BackfillColumn is Batched");
            };
            backfill::run_heavy_backfill(
                pool,
                tenant_id,
                entity_name,
                &desired.table,
                op_id,
                column,
                source_field,
                sql_type,
            )
            .await
        }
    }
}

/// Builds the SQL statement(s) for one `DdlOp`. Multiple statements only for `CreateTable`
/// (table + its framework-column types are all fixed, so it's issued as one `CREATE TABLE`) —
/// kept a `Vec` for uniformity with ops that could plausibly need more than one statement later.
fn build_sql(table: &str, op: &DdlOp) -> Vec<String> {
    let t = quote_ident(table);
    match op {
        DdlOp::CreateTable => vec![format!(
            "CREATE TABLE IF NOT EXISTS {t} (\
             id uuid PRIMARY KEY DEFAULT gen_random_uuid(), \
             tenant_id uuid NOT NULL, \
             code varchar(120), \
             status varchar(80), \
             data jsonb NOT NULL DEFAULT '{{}}'::jsonb, \
             version integer NOT NULL DEFAULT 1, \
             deleted boolean NOT NULL DEFAULT false, \
             created_at timestamptz NOT NULL DEFAULT now(), \
             updated_at timestamptz NOT NULL DEFAULT now(), \
             created_by uuid, \
             updated_by uuid)"
        )],
        DdlOp::AddColumn { name, spec } => {
            vec![format!(
                "ALTER TABLE {t} ADD COLUMN IF NOT EXISTS {} {}",
                quote_ident(name),
                spec.sql_type
            )]
        }
        DdlOp::RenameColumn { from, to } => {
            vec![format!(
                "ALTER TABLE {t} RENAME COLUMN {} TO {}",
                quote_ident(from),
                quote_ident(to)
            )]
        }
        DdlOp::AddSyncTrigger { field, sql_type } => build_sync_trigger_sql(table, field, sql_type),
        DdlOp::BackfillColumn { .. } => vec![], // Batched — handled by run_one directly, never via SQL text.
        DdlOp::CreateIndexConcurrently { name, spec } => vec![build_create_index_sql(table, name, spec)],
        DdlOp::DropIndexConcurrently { name } => {
            vec![format!("DROP INDEX CONCURRENTLY IF EXISTS {}", quote_ident(name))]
        }
        DdlOp::AddForeignKeyNotValid { name, spec } => vec![build_add_fk_sql(table, name, spec)],
        DdlOp::ValidateForeignKey { name } => {
            vec![format!("ALTER TABLE {t} VALIDATE CONSTRAINT {}", quote_ident(name))]
        }
        DdlOp::DropForeignKey { name } => {
            vec![format!(
                "ALTER TABLE {t} DROP CONSTRAINT IF EXISTS {}",
                quote_ident(name)
            )]
        }
        DdlOp::AddUnique { name, spec } => vec![build_add_unique_sql(table, name, spec)],
        DdlOp::DropUnique { name } => {
            vec![format!(
                "ALTER TABLE {t} DROP CONSTRAINT IF EXISTS {}",
                quote_ident(name)
            )]
        }
    }
}

fn build_create_index_sql(table: &str, name: &str, spec: &IndexSpec) -> String {
    let unique = if spec.unique { "UNIQUE " } else { "" };
    let using = spec.using.as_deref().map(|m| format!("USING {m} ")).unwrap_or_default();
    format!(
        "CREATE {unique}INDEX CONCURRENTLY IF NOT EXISTS {} ON {} {using}({})",
        quote_ident(name),
        quote_ident(table),
        spec.expression
    )
}

fn build_add_fk_sql(table: &str, name: &str, spec: &FkSpec) -> String {
    format!(
        "ALTER TABLE {} ADD CONSTRAINT {} FOREIGN KEY ({}) REFERENCES {} ({}) ON DELETE {} NOT VALID",
        quote_ident(table),
        quote_ident(name),
        quote_ident(&spec.column),
        quote_ident(&spec.ref_table),
        quote_ident(&spec.ref_column),
        spec.on_delete.as_sql(),
    )
}

fn build_add_unique_sql(table: &str, name: &str, spec: &UniqueSpec) -> String {
    let cols = spec
        .columns
        .iter()
        .map(|c| quote_ident(c))
        .collect::<Vec<_>>()
        .join(", ");
    format!(
        "ALTER TABLE {} ADD CONSTRAINT {} UNIQUE ({cols})",
        quote_ident(table),
        quote_ident(name)
    )
}

/// The trigger function name is derived from `(table, field)` so two different promoted fields
/// on the same table never collide, and re-running this op (crash-resume) is a plain
/// `CREATE OR REPLACE` — never "already exists" errors.
fn build_sync_trigger_sql(table: &str, field: &str, sql_type: &str) -> Vec<String> {
    let fn_name = quote_ident(&format!("sync_{table}_{field}"));
    let trigger_name = quote_ident(&format!("trg_sync_{table}_{field}"));
    let column = quote_ident(field);
    let field_literal = quote_literal(field);
    vec![
        format!(
            "CREATE OR REPLACE FUNCTION {fn_name}() RETURNS trigger AS $$ \
             BEGIN NEW.{column} := (NEW.data ->> {field_literal})::{sql_type}; RETURN NEW; END; \
             $$ LANGUAGE plpgsql"
        ),
        format!(
            "CREATE OR REPLACE TRIGGER {trigger_name} BEFORE INSERT OR UPDATE ON {} \
             FOR EACH ROW EXECUTE FUNCTION {fn_name}()",
            quote_ident(table)
        ),
    ]
}
