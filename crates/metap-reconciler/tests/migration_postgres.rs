//! E2E tests for `migration`/`quarantine` against a real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use metap_reconciler::migration::{preflight, run_migration, MigrationOp, QuarantinePolicy};
use metap_reconciler::quarantine;
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

async fn make_table(pool: &PgPool, table: &str) {
    sqlx::query(&format!("DROP TABLE IF EXISTS \"{table}\" CASCADE"))
        .execute(pool)
        .await
        .unwrap();
    sqlx::query(&format!("DROP TABLE IF EXISTS \"{table}_quarantine\" CASCADE"))
        .execute(pool)
        .await
        .unwrap();
    sqlx::query(&format!(
        "CREATE TABLE \"{table}\" (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, \
         data jsonb NOT NULL DEFAULT '{{}}'::jsonb)"
    ))
    .execute(pool)
    .await
    .unwrap();
}

async fn insert_row(pool: &PgPool, table: &str, tenant_id: Uuid, amount: &str) -> Uuid {
    sqlx::query_scalar(&format!(
        "INSERT INTO \"{table}\" (tenant_id, data) VALUES ($1, jsonb_build_object('amount', $2::text)) RETURNING id"
    ))
    .bind(tenant_id)
    .bind(amount)
    .fetch_one(pool)
    .await
    .unwrap()
}

/// §4.4: preflight is a pure count, never mutates.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn preflight_counts_bad_rows_without_touching_data() {
    let pool = connect().await;
    let table = "test_migration_preflight";
    make_table(&pool, table).await;
    let tenant_id = Uuid::new_v4();

    insert_row(&pool, table, tenant_id, "123.45").await;
    insert_row(&pool, table, tenant_id, "not-a-number").await;
    insert_row(&pool, table, tenant_id, "also-bad").await;

    let op = MigrationOp::WidenType {
        field: "amount".to_string(),
        to_sql_type: "numeric(18,4)".to_string(),
    };
    let report = preflight(&pool, table, &op).await.unwrap();
    assert_eq!(report.bad_rows, 2);

    let still_three: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM \"{table}\""))
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(still_three, 3, "preflight must never mutate data");

    sqlx::query(&format!("DROP TABLE \"{table}\""))
        .execute(&pool)
        .await
        .unwrap();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn block_policy_refuses_when_bad_rows_exist() {
    let pool = connect().await;
    let table = "test_migration_block";
    make_table(&pool, table).await;
    let tenant_id = Uuid::new_v4();
    insert_row(&pool, table, tenant_id, "bad").await;

    let op = MigrationOp::WidenType {
        field: "amount".to_string(),
        to_sql_type: "numeric(18,4)".to_string(),
    };
    let err = run_migration(
        &pool,
        tenant_id,
        "test.migration_block",
        table,
        &op,
        &QuarantinePolicy::Block,
        "m1",
    )
    .await
    .unwrap_err();
    assert!(err.to_string().contains("Block"));

    sqlx::query(&format!("DROP TABLE \"{table}\""))
        .execute(&pool)
        .await
        .unwrap();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn coerce_policy_uses_fallback_for_bad_rows_and_transforms_good_ones() {
    let pool = connect().await;
    let table = "test_migration_coerce";
    make_table(&pool, table).await;
    let tenant_id = Uuid::new_v4();
    let good = insert_row(&pool, table, tenant_id, "42.50").await;
    let bad = insert_row(&pool, table, tenant_id, "garbage").await;

    let op = MigrationOp::WidenType {
        field: "amount".to_string(),
        to_sql_type: "numeric(18,4)".to_string(),
    };
    let policy = QuarantinePolicy::Coerce {
        fallback: serde_json::json!(0),
    };
    run_migration(&pool, tenant_id, "test.migration_coerce", table, &op, &policy, "m1")
        .await
        .unwrap();

    let good_value: f64 = sqlx::query_scalar(&format!(
        "SELECT (data->>'amount')::float8 FROM \"{table}\" WHERE id = $1"
    ))
    .bind(good)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(good_value, 42.50);

    let bad_value: f64 = sqlx::query_scalar(&format!(
        "SELECT (data->>'amount')::float8 FROM \"{table}\" WHERE id = $1"
    ))
    .bind(bad)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(bad_value, 0.0, "bad row must get the fallback, not be dropped");

    let row_count: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM \"{table}\""))
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(row_count, 2, "Coerce never removes a row");

    sqlx::query(&format!("DROP TABLE \"{table}\""))
        .execute(&pool)
        .await
        .unwrap();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn quarantine_policy_moves_bad_rows_out_before_transforming_the_rest() {
    let pool = connect().await;
    let table = "test_migration_quarantine";
    make_table(&pool, table).await;
    let tenant_id = Uuid::new_v4();
    let good = insert_row(&pool, table, tenant_id, "7.00").await;
    let bad = insert_row(&pool, table, tenant_id, "not-numeric").await;

    let op = MigrationOp::WidenType {
        field: "amount".to_string(),
        to_sql_type: "numeric(18,4)".to_string(),
    };
    let outcome = run_migration(
        &pool,
        tenant_id,
        "test.migration_quarantine",
        table,
        &op,
        &QuarantinePolicy::Quarantine,
        "m1",
    )
    .await
    .unwrap();
    assert_eq!(outcome.quarantined, 1);
    assert_eq!(outcome.skipped_referenced, 0);

    let remaining_ids: Vec<Uuid> = sqlx::query_scalar(&format!("SELECT id FROM \"{table}\""))
        .fetch_all(&pool)
        .await
        .unwrap();
    assert_eq!(
        remaining_ids,
        vec![good],
        "the bad row must be gone from the main table"
    );

    let quarantined: (Uuid, serde_json::Value) = sqlx::query_as(&format!(
        "SELECT id, original_data FROM \"{table}_quarantine\" WHERE id = $1"
    ))
    .bind(bad)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(quarantined.0, bad);
    assert_eq!(quarantined.1["amount"], serde_json::json!("not-numeric"));

    // Resolve: a human fixes the data, it goes back into the main table.
    quarantine::resolve(&pool, table, bad, serde_json::json!({"amount": "9.99"}))
        .await
        .unwrap();
    let back_count: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM \"{table}\" WHERE id = $1"))
        .bind(bad)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(back_count, 1);

    sqlx::query(&format!("DROP TABLE \"{table}\" CASCADE"))
        .execute(&pool)
        .await
        .unwrap();
}

/// §4.5: a row referenced by another entity's `ON DELETE RESTRICT` FK must not be quarantined —
/// Postgres itself refuses the delete, and `quarantine_bad_rows` must skip it (not error out the
/// whole batch) rather than orphaning the referencing row.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn quarantine_skips_a_row_still_referenced_by_another_table() {
    let pool = connect().await;
    let parent = "test_migration_quarantine_parent";
    let child = "test_migration_quarantine_child";
    sqlx::query(&format!("DROP TABLE IF EXISTS \"{child}\" CASCADE"))
        .execute(&pool)
        .await
        .unwrap();
    make_table(&pool, parent).await;
    let tenant_id = Uuid::new_v4();
    let referenced = insert_row(&pool, parent, tenant_id, "bad-cast").await;

    sqlx::query(&format!(
        "CREATE TABLE \"{child}\" (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, \
         parent_id uuid NOT NULL REFERENCES \"{parent}\" (id) ON DELETE RESTRICT)"
    ))
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query(&format!(
        "INSERT INTO \"{child}\" (tenant_id, parent_id) VALUES ($1, $2)"
    ))
    .bind(tenant_id)
    .bind(referenced)
    .execute(&pool)
    .await
    .unwrap();

    let bad_predicate = "NOT pg_input_is_valid(t.data ->> 'amount', 'numeric')";
    let outcome = quarantine::quarantine_bad_rows(&pool, parent, bad_predicate, "m1", "test")
        .await
        .unwrap();
    assert_eq!(outcome.quarantined, 0);
    assert_eq!(outcome.skipped_referenced, 1);

    let still_there: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM \"{parent}\" WHERE id = $1"))
        .bind(referenced)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(still_there, 1, "a referenced row must not be removed");

    sqlx::query(&format!("DROP TABLE \"{child}\" CASCADE"))
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query(&format!("DROP TABLE \"{parent}\" CASCADE"))
        .execute(&pool)
        .await
        .unwrap();
}
