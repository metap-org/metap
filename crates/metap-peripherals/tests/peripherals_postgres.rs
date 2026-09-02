//! E2E tests against the repo's real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use metap_metadata::{EntityField, EntityListView, EntitySummary, FieldKind};
use metap_peripherals::{
    assign_role, check_metadata_drift, get_roles_for_user, list_users, reconcile_indexes, revoke_role,
};
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use tokio::sync::Mutex;
use uuid::Uuid;

/// Postgres documents that two concurrent `CREATE INDEX CONCURRENTLY` builds on the *same*
/// table can deadlock each other (confirmed directly against the dev Postgres here — two
/// concurrent sessions both building an index on `records` reliably deadlock, one erroring).
/// `IndexReconciler` itself never triggers this in production (`reconcile_inner` awaits each
/// `CREATE INDEX` sequentially within one process) — this is purely about two *test functions*
/// in this file each doing their own `reconcile_indexes` call and, by default, the test harness
/// running them on separate tokio tasks at the same time.
///
/// Also held around `reconcile_creates_a_trigram_index_postgres_actually_selects_for_ilike`'s
/// seed-rows/`ANALYZE`/`EXPLAIN` section (found live, 2026-08-28): that test's planner-choice
/// assertion depends on `records`' actual row count/stats at the moment `EXPLAIN` runs, which
/// `reconcile_creates_an_index_postgres_actually_selects_for_the_exact_query_form` running
/// concurrently on the same shared table (its own `CREATE INDEX CONCURRENTLY`/row churn) can
/// perturb — flaky exactly the same way one `CREATE INDEX CONCURRENTLY` racing another is, just
/// via statistics instead of a DDL deadlock. Not held around the whole test in either case, so
/// the (fast) index-exists assertions/cleanup still run concurrently.
static INDEX_BUILD_LOCK: Mutex<()> = Mutex::const_new(());

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn role_assignment_round_trip() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();

    assert!(get_roles_for_user(&pool, tenant_id, user_id).await.unwrap().is_empty());

    assign_role(&pool, tenant_id, user_id, "sales", None).await.unwrap();
    assign_role(&pool, tenant_id, user_id, "support", None).await.unwrap();
    // duplicate assign is a no-op, not an error (ON CONFLICT DO NOTHING)
    assign_role(&pool, tenant_id, user_id, "sales", None).await.unwrap();

    let mut roles = get_roles_for_user(&pool, tenant_id, user_id).await.unwrap();
    roles.sort();
    assert_eq!(roles, vec!["sales".to_string(), "support".to_string()]);

    let users = list_users(&pool, tenant_id).await.unwrap();
    assert_eq!(users.len(), 1);
    assert_eq!(users[0].user_id, user_id);

    revoke_role(&pool, tenant_id, user_id, "sales").await.unwrap();
    let roles = get_roles_for_user(&pool, tenant_id, user_id).await.unwrap();
    assert_eq!(roles, vec!["support".to_string()]);

    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

fn summary(name: &str, version: &str) -> EntitySummary {
    EntitySummary {
        name: name.to_string(),
        label: "Test".to_string(),
        fields: vec![],
        list_views: vec![],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: version.to_string(),
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn metadata_drift_records_first_boot_then_detects_change() {
    let pool = connect().await;
    let entity_name = format!("test.drift.{}", Uuid::new_v4());

    check_metadata_drift(&pool, &[summary(&entity_name, "hash-v1")]).await;
    let stored: String = sqlx::query_scalar("SELECT hash FROM metadata_versions WHERE entity_name = $1")
        .bind(&entity_name)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(stored, "hash-v1");

    // second call with a different hash — drift is logged (stderr), row is updated either way
    check_metadata_drift(&pool, &[summary(&entity_name, "hash-v2")]).await;
    let stored: String = sqlx::query_scalar("SELECT hash FROM metadata_versions WHERE entity_name = $1")
        .bind(&entity_name)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(stored, "hash-v2");

    sqlx::query("DELETE FROM metadata_versions WHERE entity_name = $1")
        .bind(&entity_name)
        .execute(&pool)
        .await
        .ok();
}

fn indexed_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: Some(true),
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

fn fts_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: Some(true),
        search_mode: Some("fts".to_string()),
        sortable: None,
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

fn substring_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: Some(true),
        search_mode: None, // "substring" is the default when searchable: true and not "fts"
        sortable: None,
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reconcile_creates_an_index_postgres_actually_selects_for_the_exact_query_form() {
    let pool = connect().await;
    // Short suffix, not a full UUID — a realistic entity name length (e.g. "crm.customers"),
    // since `gin_records_<entity>_<field>`/`idx_records_<entity>_<field>` must stay under
    // Postgres's 63-byte identifier limit or the actual stored name silently truncates,
    // which is a test-fixture concern, not something `index_reconciler.rs` needs to solve.
    let entity_name = format!("test.idx{:x}", Uuid::new_v4().as_u128() % 0xFFFFFF);

    let entity = EntitySummary {
        name: entity_name.clone(),
        label: "Test".to_string(),
        fields: vec![indexed_field("sku")],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: "v1".to_string(),
    };

    let index_name = format!("idx_records_{}_sku", entity_name.replace('.', "_"));
    let plan = {
        // See `INDEX_BUILD_LOCK`'s doc comment — held for this whole block (not just
        // `reconcile_indexes`) for the same cross-test-interference reason as the trigram test
        // below, even though this query's own `entity =` predicate is unique per test run.
        let _guard = INDEX_BUILD_LOCK.lock().await;
        reconcile_indexes(&pool, &[entity]).await;

        let exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
            .bind(&index_name)
            .fetch_optional(&pool)
            .await
            .unwrap();
        assert!(exists.is_some(), "expected index {index_name} to have been created");

        // Same rigor as the original TS test suite: confirm Postgres's planner actually
        // selects this index for the exact expression form QueryPlanner emits
        // (jsonb_extract_path_text), not just that the index exists.
        //
        // `SET LOCAL enable_seqscan = off` (transaction-scoped, rolled back below — never leaks
        // into another test via a reused pooled connection): this test's synthetic entity has
        // zero rows, and `records` itself may hold anywhere from a few hundred to tens of
        // thousands of other e2e tests' rows depending on scheduling under parallel CI execution
        // — a seq scan over "whatever records happens to hold right now" is a real cost-based
        // choice the planner is entitled to make and has nothing to do with what this test
        // actually checks (found live, flaky in CI: passed or failed per-run depending on how
        // much other tests had inserted by the time this ran). Forcing seqscan off isolates the
        // real question — is the index's expression form actually compatible with this exact
        // WHERE clause — from ambient data volume, which
        // `docs/roadmap/17-metadata-workflow-engine.md`'s benchmark suite already covers at
        // realistic scale.
        let mut tx = pool.begin().await.unwrap();
        sqlx::query("SET LOCAL enable_seqscan = off")
            .execute(&mut *tx)
            .await
            .unwrap();
        let explain_rows = sqlx::query(&format!(
            "EXPLAIN SELECT id FROM records WHERE entity = '{entity_name}' AND deleted = false \
             AND jsonb_extract_path_text(data, 'sku') = 'X'"
        ))
        .fetch_all(&mut *tx)
        .await
        .unwrap();
        tx.rollback().await.ok();

        explain_rows
            .iter()
            .map(|r| r.get::<String, _>(0))
            .collect::<Vec<_>>()
            .join("\n")
    };
    assert!(
        plan.contains(&index_name),
        "expected query plan to use {index_name}, got:\n{plan}"
    );

    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {index_name}"))
        .execute(&pool)
        .await
        .ok();

    // idempotent: reconciling again when the index already exists is a no-op, not an error
    let entity_again = EntitySummary {
        name: entity_name.clone(),
        label: "Test".to_string(),
        fields: vec![fts_field("description")],
        list_views: vec![],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: "v1".to_string(),
    };
    {
        let _guard = INDEX_BUILD_LOCK.lock().await;
        reconcile_indexes(&pool, &[entity_again]).await;
    }
    let gin_index_name = format!("gin_records_{}_description", entity_name.replace('.', "_"));
    let gin_exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
        .bind(&gin_index_name)
        .fetch_optional(&pool)
        .await
        .unwrap();
    assert!(
        gin_exists.is_some(),
        "expected GIN index {gin_index_name} to have been created"
    );
    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {gin_index_name}"))
        .execute(&pool)
        .await
        .ok();
}

/// `searchable: true` with no `search_mode` (or `search_mode: "substring"`) used to have zero
/// index support — a leading-wildcard `ILIKE '%value%'` can't use a plain B-tree. Confirms
/// `reconcile_indexes` builds a `pg_trgm` GIN index (`crates/migrations/0016_pg_trgm_extension.sql`)
/// that Postgres's planner actually picks for the exact `ILIKE` form `condition_to_sql.rs` emits.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reconcile_creates_a_trigram_index_postgres_actually_selects_for_ilike() {
    let pool = connect().await;
    let entity_name = format!("test.idx{:x}", Uuid::new_v4().as_u128() % 0xFFFFFF);

    let entity = EntitySummary {
        name: entity_name.clone(),
        label: "Test".to_string(),
        fields: vec![substring_field("title")],
        list_views: vec![],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: "v1".to_string(),
    };

    let index_name = format!("trgm_records_{}_title", entity_name.replace('.', "_"));
    let plan = {
        // See `INDEX_BUILD_LOCK`'s doc comment: held for this whole block, not just
        // `reconcile_indexes`, because the sibling exact-match test's own row churn on the same
        // shared `records` table can perturb this test's planner-choice assertion below.
        let _guard = INDEX_BUILD_LOCK.lock().await;
        reconcile_indexes(&pool, &[entity]).await;

        let exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
            .bind(&index_name)
            .fetch_optional(&pool)
            .await
            .unwrap();
        assert!(
            exists.is_some(),
            "expected trigram index {index_name} to have been created"
        );

        // A GIN trigram index only wins the planner's cost comparison at real volume — confirmed
        // empirically (found live, 2026-08-28): at a handful of rows for this entity, Postgres
        // reliably prefers the generic `(tenant_id, entity, created_at)` composite index (already
        // narrows to this entity essentially for free) over the trigram GIN index (real per-scan
        // overhead of its own), even with `enable_seqscan = off` forcing *some* index to be used
        // — the composite index + a cheap in-memory filter on a handful of candidate rows beats
        // firing up a GIN scan for the same handful of rows. 10,000 rows was the observed
        // crossover point locally; seeding 15,000 for margin. This matches production reality too
        // (the trigram index is genuinely only the right choice once per-entity row count grows
        // large — see the 500K-1M-row benchmark in
        // `docs/roadmap/17-metadata-workflow-engine.md`), not an artifact worked around here.
        // `enable_seqscan = off` is still needed on top: without it, an entirely empty *whole*
        // `records` table (a fresh CI database before other e2e tests have inserted anything)
        // makes a trivial Seq Scan of everything cheaper than any index, regardless of this
        // entity's own row count.
        sqlx::query(
            "INSERT INTO records (id, tenant_id, entity, status, data, version, deleted, created_at, updated_at) \
             SELECT gen_random_uuid(), $1, $2, 'active', \
                    jsonb_build_object('title', 'row number ' || g || ' something else'), 1, false, now(), now() \
             FROM generate_series(1, 15000) g",
        )
        .bind(Uuid::new_v4())
        .bind(&entity_name)
        .execute(&pool)
        .await
        .unwrap();
        sqlx::query("ANALYZE records").execute(&pool).await.unwrap();

        let mut tx = pool.begin().await.unwrap();
        sqlx::query("SET LOCAL enable_seqscan = off")
            .execute(&mut *tx)
            .await
            .unwrap();
        let explain_rows = sqlx::query(&format!(
            "EXPLAIN SELECT id FROM records WHERE entity = '{entity_name}' AND deleted = false \
             AND jsonb_extract_path_text(data, 'title') ILIKE '%widget%'"
        ))
        .fetch_all(&mut *tx)
        .await
        .unwrap();
        tx.rollback().await.ok();

        explain_rows
            .iter()
            .map(|r| r.get::<String, _>(0))
            .collect::<Vec<_>>()
            .join("\n")
    };
    assert!(
        plan.contains(&index_name),
        "expected query plan to use {index_name}, got:\n{plan}"
    );

    sqlx::query("DELETE FROM records WHERE entity = $1")
        .bind(&entity_name)
        .execute(&pool)
        .await
        .ok();
    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {index_name}"))
        .execute(&pool)
        .await
        .ok();
}

/// Regression test for a real bug found in code review (2026-08-22): `reconcile_inner`'s
/// per-field `.await?` used to propagate the *first* index-build failure straight out of the
/// whole function, silently abandoning reconciliation for every entity/field that came after it
/// in the loop — e.g. a deployment that forgot to run a migration adding a new index type
/// (like `pg_trgm`) before restarting would lose indexing for the entire rest of that boot, not
/// just the one field that genuinely needed the missing migration. Forces a deterministic
/// failure for one entity's index (a same-named table already occupies that relation name —
/// Postgres shares one namespace for tables/indexes per schema, so `CREATE INDEX` on that name
/// fails with "relation already exists") and confirms a *second*, unrelated entity later in the
/// same `reconcile_indexes` call still gets its index built.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reconcile_continues_past_one_entitys_index_failure() {
    let pool = connect().await;
    let entity_a_name = format!("test.idx{:x}", Uuid::new_v4().as_u128() % 0xFFFFFF);
    let entity_b_name = format!("test.idx{:x}", Uuid::new_v4().as_u128() % 0xFFFFFF);

    let blocked_index_name = format!("idx_records_{}_sku", entity_a_name.replace('.', "_"));
    let ok_index_name = format!("idx_records_{}_sku", entity_b_name.replace('.', "_"));

    // Occupy entity A's would-be index name with a table instead, so `CREATE INDEX` for it
    // fails with a real Postgres error ("relation ... already exists").
    sqlx::query(&format!("CREATE TABLE {blocked_index_name} (id int)"))
        .execute(&pool)
        .await
        .unwrap();

    let entity_a = EntitySummary {
        name: entity_a_name.clone(),
        label: "Test".to_string(),
        fields: vec![indexed_field("sku")],
        list_views: vec![],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: "v1".to_string(),
    };
    let entity_b = EntitySummary {
        name: entity_b_name.clone(),
        label: "Test".to_string(),
        fields: vec![indexed_field("sku")],
        list_views: vec![],
        workflow: None,
        related_views: vec![],
        field_display_hints: vec![],
        version: "v1".to_string(),
    };

    {
        let _guard = INDEX_BUILD_LOCK.lock().await;
        reconcile_indexes(&pool, &[entity_a, entity_b]).await;
    }

    let ok_exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
        .bind(&ok_index_name)
        .fetch_optional(&pool)
        .await
        .unwrap();
    assert!(
        ok_exists.is_some(),
        "entity B's index must still be built even though entity A's failed"
    );

    sqlx::query(&format!("DROP TABLE IF EXISTS {blocked_index_name}"))
        .execute(&pool)
        .await
        .ok();
    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {ok_index_name}"))
        .execute(&pool)
        .await
        .ok();
}
