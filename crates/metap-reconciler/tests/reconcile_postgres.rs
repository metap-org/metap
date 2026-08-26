//! E2E tests running `reconcile()` against a real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention (unit tests
//! never touch a DB; these run explicitly via `cargo test -- --ignored`).

use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, FieldStorage};
use metap_reconciler::reconcile;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

fn plain_field(name: &str, kind: FieldKind) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind,
        required: None,
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: None,
        search_mode: None,
        sortable: None,
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
    }
}

fn entity(name: &str, fields: Vec<EntityField>) -> EntityDefinition {
    EntityDefinition {
        name: name.to_string(),
        label: name.to_string(),
        table_name: "records".to_string(),
        fields,
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

/// `table` is a bare name — every table-per-entity table lives in `metap_reconciler::ENTITY_SCHEMA`
/// (`"entities"`), never `public`.
async fn drop_table_if_exists(pool: &PgPool, table: &str) {
    sqlx::query(&format!(
        "DROP TABLE IF EXISTS \"{}\".\"{table}\" CASCADE",
        metap_reconciler::ENTITY_SCHEMA
    ))
    .execute(pool)
    .await
    .unwrap();
}

/// The single most important correctness property of a level-triggered reconciler
/// (`docs/multi-tenant-platform-design.md` §5.1): reconciling an unchanged desired state twice
/// in a row must produce zero ops the second time. If this doesn't hold, every boot/republish
/// would keep re-running DDL forever instead of converging.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reconcile_converges_to_zero_ops_on_a_second_pass() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = "test.reconciler_convergence";
    drop_table_if_exists(&pool, "test_reconciler_convergence").await;

    let mut indexed = plain_field("state", FieldKind::Enum);
    indexed.indexed = Some(true);
    indexed.enum_values = Some(vec!["open".to_string(), "closed".to_string()]);
    let mut searchable = plain_field("title", FieldKind::String);
    searchable.searchable = Some(true);
    let mut fts_field = plain_field("description", FieldKind::String);
    fts_field.searchable = Some(true);
    fts_field.search_mode = Some("fts".to_string());

    let def = entity(entity_name, vec![indexed, searchable, fts_field]);

    let first = reconcile(&pool, tenant_id, &def, &[]).await.unwrap();
    assert!(first.ops_applied > 0, "first reconcile must actually do work");
    assert_eq!(first.table, "entities.test_reconciler_convergence");

    let second = reconcile(&pool, tenant_id, &def, &[]).await.unwrap();
    assert_eq!(
        second.ops_applied, 0,
        "second reconcile against the same desired state must be a no-op"
    );

    // A third pass, for good measure — convergence must hold indefinitely, not just once.
    let third = reconcile(&pool, tenant_id, &def, &[]).await.unwrap();
    assert_eq!(third.ops_applied, 0);

    drop_table_if_exists(&pool, "test_reconciler_convergence").await;
}

/// `storage: column` end to end: a field promoted to a real column on an entity that already
/// has existing rows must have those rows correctly backfilled (§5.7), and the sync trigger
/// must keep a newly-written row consistent without needing another reconcile.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn storage_column_backfills_existing_rows_and_syncs_new_ones() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = "test.reconciler_storage_column";
    drop_table_if_exists(&pool, "test_reconciler_storage_column").await;

    // Pass 1: reconcile with the field still JSONB-only, then insert rows directly (as if
    // CrudService had been writing to the table for a while before the field was promoted).
    let def_v1 = entity(entity_name, vec![plain_field("amount", FieldKind::Money)]);
    reconcile(&pool, tenant_id, &def_v1, &[]).await.unwrap();

    for amount in ["12.50", "7.25", "100.00"] {
        sqlx::query("INSERT INTO entities.test_reconciler_storage_column (tenant_id, data) VALUES ($1, jsonb_build_object('amount', $2::text))")
            .bind(tenant_id)
            .bind(amount)
            .execute(&pool)
            .await
            .unwrap();
    }

    // Pass 2: promote `amount` to a real column — must backfill the 3 existing rows.
    let mut promoted = plain_field("amount", FieldKind::Money);
    promoted.indexed = Some(true);
    promoted.storage = Some(FieldStorage::Column);
    let def_v2 = entity(entity_name, vec![promoted]);
    let outcome = reconcile(&pool, tenant_id, &def_v2, &[]).await.unwrap();
    assert!(outcome.ops_applied > 0);

    let backfilled: Vec<(f64,)> =
        sqlx::query_as("SELECT amount::float8 FROM entities.test_reconciler_storage_column ORDER BY amount")
            .fetch_all(&pool)
            .await
            .unwrap();
    assert_eq!(
        backfilled,
        vec![(7.25,), (12.50,), (100.00,)],
        "all 3 pre-existing rows must be backfilled"
    );

    // A new row written directly (bypassing any application code that might know about the
    // promoted column) must still end up with the real column populated, via the sync trigger.
    sqlx::query("INSERT INTO entities.test_reconciler_storage_column (tenant_id, data) VALUES ($1, jsonb_build_object('amount', '42.00'))")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .unwrap();
    let synced: (f64,) =
        sqlx::query_as("SELECT amount::float8 FROM entities.test_reconciler_storage_column WHERE amount = 42.00")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(synced.0, 42.00);

    // Pass 3: reconcile again — must converge to zero ops now that backfill is `completed`.
    let third = reconcile(&pool, tenant_id, &def_v2, &[]).await.unwrap();
    assert_eq!(
        third.ops_applied, 0,
        "backfill must be recognized as already-done, not re-queued"
    );

    drop_table_if_exists(&pool, "test_reconciler_storage_column").await;
}

/// Regression for the finding in `AUDIT_2.md`: `backfill::run_batched_update`'s batch-select had
/// no `tenant_id` filter at all, relying purely on the convention "a dedicated table only ever
/// holds one tenant's rows" — never checked in code. Simulates the convention being violated (two
/// tenants' rows sharing one physical table, which should never happen in practice today but
/// wasn't prevented either) and confirms a backfill scoped to tenant A leaves tenant B's row
/// completely untouched, rather than silently backfilling across the tenant boundary.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn storage_column_backfill_does_not_touch_another_tenants_row_in_a_shared_table() {
    let pool = connect().await;
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();
    let entity_name = "test.reconciler_storage_column_multitenant";
    drop_table_if_exists(&pool, "test_reconciler_storage_column_multitenant").await;

    let def_v1 = entity(entity_name, vec![plain_field("amount", FieldKind::Money)]);
    reconcile(&pool, tenant_a, &def_v1, &[]).await.unwrap();

    sqlx::query(
        "INSERT INTO entities.test_reconciler_storage_column_multitenant (tenant_id, data) \
         VALUES ($1, jsonb_build_object('amount', '12.50'::text))",
    )
    .bind(tenant_a)
    .execute(&pool)
    .await
    .unwrap();
    // Simulates the convention being violated: tenant B's row lives in the *same* physical
    // table (never provisioned/reconciled for tenant B — just directly inserted, matching how
    // a real cross-tenant leak would look: a row that shouldn't be here at all).
    sqlx::query(
        "INSERT INTO entities.test_reconciler_storage_column_multitenant (tenant_id, data) \
         VALUES ($1, jsonb_build_object('amount', '999.99'::text))",
    )
    .bind(tenant_b)
    .execute(&pool)
    .await
    .unwrap();

    let mut promoted = plain_field("amount", FieldKind::Money);
    promoted.indexed = Some(true);
    promoted.storage = Some(FieldStorage::Column);
    let def_v2 = entity(entity_name, vec![promoted]);
    reconcile(&pool, tenant_a, &def_v2, &[]).await.unwrap();

    let tenant_a_amount: (Option<f64>,) = sqlx::query_as(
        "SELECT amount::float8 FROM entities.test_reconciler_storage_column_multitenant WHERE tenant_id = $1",
    )
    .bind(tenant_a)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(tenant_a_amount.0, Some(12.50), "tenant A's own row must be backfilled");

    let tenant_b_amount: (Option<f64>,) = sqlx::query_as(
        "SELECT amount::float8 FROM entities.test_reconciler_storage_column_multitenant WHERE tenant_id = $1",
    )
    .bind(tenant_b)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(
        tenant_b_amount.0, None,
        "a backfill scoped to tenant A must never touch tenant B's row, even sharing one physical table"
    );

    drop_table_if_exists(&pool, "test_reconciler_storage_column_multitenant").await;
}

/// §3.3 — a `Reference` field whose `ref_entity` already has its own table gets a real FK.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reference_field_gets_a_real_fk_once_both_entities_are_tables() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    drop_table_if_exists(&pool, "test_reconciler_departments").await;
    drop_table_if_exists(&pool, "test_reconciler_employees").await;

    let departments = entity(
        "test.reconciler_departments",
        vec![plain_field("name", FieldKind::String)],
    );
    reconcile(&pool, tenant_id, &departments, &[]).await.unwrap();

    let mut dept_ref = plain_field("departmentId", FieldKind::Reference);
    dept_ref.ref_entity = Some("test.reconciler_departments".to_string());
    let employees = entity("test.reconciler_employees", vec![dept_ref]);
    let outcome = reconcile(&pool, tenant_id, &employees, &[]).await.unwrap();
    assert!(outcome.ops_applied > 0);

    let fk_exists: bool = sqlx::query_scalar(
        "SELECT EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid \
         WHERE t.relname = 'test_reconciler_employees' AND c.contype = 'f' AND c.convalidated)",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    assert!(
        fk_exists,
        "FK to test_reconciler_departments must exist and be validated"
    );

    drop_table_if_exists(&pool, "test_reconciler_employees").await;
    drop_table_if_exists(&pool, "test_reconciler_departments").await;
}
