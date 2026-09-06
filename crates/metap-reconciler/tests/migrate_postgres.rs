//! E2E tests for `migrate` against a real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention (unit tests
//! never touch a DB; these run explicitly via `cargo test -- --ignored`).

use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, FieldStorage};
use metap_reconciler::migrate_generic_to_dedicated;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(5).connect(&database_url).await.unwrap()
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
        computed: None,
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
            required_fields: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

async fn drop_table_if_exists(pool: &PgPool, table: &str) {
    sqlx::query(&format!(
        "DROP TABLE IF EXISTS \"{}\".\"{table}\" CASCADE",
        metap_reconciler::ENTITY_SCHEMA
    ))
    .execute(pool)
    .await
    .unwrap();
}

async fn cleanup_records(pool: &PgPool, tenant_id: Uuid, entity_name: &str) {
    sqlx::query("DELETE FROM records WHERE tenant_id = $1 AND entity = $2")
        .bind(tenant_id)
        .bind(entity_name)
        .execute(pool)
        .await
        .unwrap();
    sqlx::query("DELETE FROM reconciler_backfill_progress WHERE tenant_id = $1 AND entity_name = $2")
        .bind(tenant_id)
        .bind(entity_name)
        .execute(pool)
        .await
        .unwrap();
}

/// Core acceptance criterion from the brief: migrating an entity already living on `records`
/// moves every row onto its own dedicated table without losing any, preserving `version`
/// (optimistic locking) exactly, and correctly populating a `storage: column`-promoted field via
/// `reconcile()`'s own sync trigger (never touched directly by the copy — it only writes `data`).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn migrates_existing_records_rows_onto_a_dedicated_table_without_loss() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = "test.migrate_customers";
    drop_table_if_exists(&pool, "test_migrate_customers").await;
    cleanup_records(&pool, tenant_id, entity_name).await;

    let mut amount = plain_field("balance", FieldKind::Money);
    amount.storage = Some(FieldStorage::Column);
    let def = entity(entity_name, vec![plain_field("name", FieldKind::String), amount]);

    // Seed rows directly on `records`, as if `CrudService` had been writing to it for a while —
    // including a non-default `version` (optimistic-locking counter) to verify it survives the
    // copy unchanged, and a `deleted = true` row to verify a soft-deleted record is not silently
    // dropped by the migration (it should still be migrated — the workflow/entity layer, not this
    // copy, is what interprets `deleted`).
    let ids: Vec<Uuid> = vec![Uuid::new_v4(), Uuid::new_v4(), Uuid::new_v4()];
    sqlx::query(
        "INSERT INTO records (id, tenant_id, entity, code, status, data, version, deleted) VALUES \
         ($1, $4, $5, 'CUST-1', 'active', jsonb_build_object('name', 'Alice', 'balance', '12.50'), 1, false), \
         ($2, $4, $5, 'CUST-2', 'active', jsonb_build_object('name', 'Bob', 'balance', '7.25'), 5, false), \
         ($3, $4, $5, 'CUST-3', 'active', jsonb_build_object('name', 'Carol', 'balance', '0.00'), 2, true)",
    )
    .bind(ids[0])
    .bind(ids[1])
    .bind(ids[2])
    .bind(tenant_id)
    .bind(entity_name)
    .execute(&pool)
    .await
    .unwrap();

    let outcome = migrate_generic_to_dedicated(&pool, tenant_id, &def, "records").await.unwrap();
    assert_eq!(outcome.table, "entities.test_migrate_customers");
    assert_eq!(outcome.copy.rows_scanned, 3);

    let source_count: i64 = sqlx::query_scalar("SELECT count(*) FROM records WHERE tenant_id = $1 AND entity = $2")
        .bind(tenant_id)
        .bind(entity_name)
        .fetch_one(&pool)
        .await
        .unwrap();
    let dest_count: i64 = sqlx::query_scalar("SELECT count(*) FROM entities.test_migrate_customers WHERE tenant_id = $1")
        .bind(tenant_id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(source_count, 3, "source rows are left in place — this is a copy, not a move");
    assert_eq!(dest_count, 3, "no record lost");

    let rows: Vec<(Uuid, i32, bool, f64, String)> = sqlx::query_as(
        "SELECT id, version, deleted, balance::float8, data ->> 'name' FROM entities.test_migrate_customers ORDER BY code",
    )
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(rows.len(), 3);
    assert_eq!(rows[0], (ids[0], 1, false, 12.50, "Alice".to_string()));
    assert_eq!(rows[1], (ids[1], 5, false, 7.25, "Bob".to_string()));
    assert_eq!(rows[2], (ids[2], 2, true, 0.00, "Carol".to_string()));

    // A second run must be a safe no-op (idempotent resume / accidental re-invocation) — zero new
    // rows, same counts.
    let second = migrate_generic_to_dedicated(&pool, tenant_id, &def, "records").await.unwrap();
    assert_eq!(second.copy.rows_scanned, 0, "already-migrated rows are not rescanned");
    let dest_count_again: i64 =
        sqlx::query_scalar("SELECT count(*) FROM entities.test_migrate_customers WHERE tenant_id = $1")
            .bind(tenant_id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(dest_count_again, 3);

    cleanup_records(&pool, tenant_id, entity_name).await;
    drop_table_if_exists(&pool, "test_migrate_customers").await;
}

/// Simulates a crash partway through a multi-batch copy (by seeding the checkpoint row directly,
/// as if a previous process had already committed the first batch and then died before finishing)
/// and verifies resume picks up exactly the remaining rows rather than re-scanning or skipping any.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn resumes_from_a_saved_checkpoint_after_a_simulated_crash() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = "test.migrate_resume";
    drop_table_if_exists(&pool, "test_migrate_resume").await;
    cleanup_records(&pool, tenant_id, entity_name).await;

    let def = entity(entity_name, vec![plain_field("name", FieldKind::String)]);

    // First, actually create the dedicated table (mirrors what a real crashed run would have
    // already done via `reconcile()` before dying mid-copy).
    metap_reconciler::reconcile(&pool, tenant_id, &def, &[]).await.unwrap();

    let mut ids: Vec<Uuid> = (0..3).map(|_| Uuid::new_v4()).collect();
    ids.sort();
    for (i, id) in ids.iter().enumerate() {
        sqlx::query(
            "INSERT INTO records (id, tenant_id, entity, data, version, deleted) \
             VALUES ($1, $2, $3, jsonb_build_object('name', $4::text), 1, false)",
        )
        .bind(id)
        .bind(tenant_id)
        .bind(entity_name)
        .bind(format!("row-{i}"))
        .execute(&pool)
        .await
        .unwrap();
    }

    // Manually pre-copy the first row and record a checkpoint past it, as if a prior crashed run
    // had already committed exactly that batch.
    sqlx::query(
        "INSERT INTO entities.test_migrate_resume (id, tenant_id, data, version, deleted) \
         SELECT id, tenant_id, data, version, deleted FROM records WHERE id = $1",
    )
    .bind(ids[0])
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query(
        "INSERT INTO reconciler_backfill_progress (tenant_id, entity_name, op_id, cursor_id, completed, updated_at) \
         VALUES ($1, $2, $3, $4, false, now())",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(metap_reconciler::MIGRATE_OP_ID)
    .bind(ids[0])
    .execute(&pool)
    .await
    .unwrap();

    let outcome = migrate_generic_to_dedicated(&pool, tenant_id, &def, "records").await.unwrap();
    assert_eq!(
        outcome.copy.rows_scanned, 2,
        "resume must only scan the 2 rows past the saved checkpoint, not all 3"
    );

    let dest_count: i64 = sqlx::query_scalar("SELECT count(*) FROM entities.test_migrate_resume WHERE tenant_id = $1")
        .bind(tenant_id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(dest_count, 3, "all 3 rows present after resume — none lost, none duplicated");

    cleanup_records(&pool, tenant_id, entity_name).await;
    drop_table_if_exists(&pool, "test_migrate_resume").await;
}
