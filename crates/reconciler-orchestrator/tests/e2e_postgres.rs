//! E2E tests running the full claim → resolve published low-code definition → `reconcile()` →
//! record-outcome loop against a real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use std::sync::Arc;
use std::time::Duration;

use metap_control::{EnvStore, PostgresTenantRegistry, RegistryCache, Router};
use metap_lowcode::LowCodeEntityDefinition;
use metap_metadata::{EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_reconciler::orchestrator::enqueue_deployment;
use reconciler_orchestrator::{run_once, run_tick, OrchestratorConfig};
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

/// Same `test.<prefix>_<hex>` convention `metap-lowcode`'s tests use — but unlike those (pure
/// JSON metadata, any length is fine), a happy-path test here turns this into a real Postgres
/// table identifier (`metap_reconciler::table_name_for`), so `prefix` must stay short enough
/// that `"test_" + prefix + "_" + 32 hex chars` doesn't exceed Postgres's 63-byte identifier
/// limit — found live: an earlier `"orchestrator_e2e_happy_path"` prefix silently truncated at
/// 63 bytes, so the test's own `information_schema.tables` lookup (unaware of the truncation)
/// never found the table `reconcile()` had actually created under the shorter, truncated name.
fn entity_name(prefix: &str) -> String {
    assert!(
        prefix.len() <= 25,
        "prefix too long for a real Postgres table identifier once table_name_for/ENTITY_SCHEMA wrap it"
    );
    format!("test.{prefix}_{}", Uuid::new_v4().simple())
}

/// `entity_filter` scopes each test to only its own entity — this table is shared across every
/// e2e test the Rust harness runs concurrently, so an unscoped (`None`) claim here (fine, and
/// the production default, in `main.rs`) would risk one test claiming another's pending row.
fn config(worker_id: &str, entity_filter: &str) -> OrchestratorConfig {
    OrchestratorConfig {
        worker_id: worker_id.to_string(),
        poll_interval: Duration::from_millis(50),
        batch_limit: 10,
        max_attempts: 3,
        concurrency_limit: 2,
        entity_name_filter: Some(entity_filter.to_string()),
    }
}

async fn router_for(pool: &PgPool) -> Router {
    let tenant_registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    Router::new(pool.clone(), RegistryCache::new(tenant_registry), Arc::new(EnvStore))
}

/// Same throwaway-database pattern `metap-control/tests/provisioning_postgres.rs` uses to test
/// `DedicatedDb` for real — duplicated here rather than shared, no test-support crate exists yet
/// for e2e helpers to live in (same reasoning that file's own copy gives).
async fn create_throwaway_database(database_url: &str, name: &str) -> String {
    let (base, _dbname) = database_url
        .rsplit_once('/')
        .expect("DATABASE_URL must end in /<dbname>");
    let admin_pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(&format!("{base}/postgres"))
        .await
        .expect("connect to postgres maintenance db");
    sqlx::query(&format!("CREATE DATABASE \"{name}\""))
        .execute(&admin_pool)
        .await
        .expect("create throwaway database");
    admin_pool.close().await;
    format!("{base}/{name}")
}

async fn drop_throwaway_database(database_url: &str, name: &str) {
    let (base, _dbname) = database_url
        .rsplit_once('/')
        .expect("DATABASE_URL must end in /<dbname>");
    let Ok(admin_pool) = PgPoolOptions::new()
        .max_connections(1)
        .connect(&format!("{base}/postgres"))
        .await
    else {
        return;
    };
    let _ = sqlx::query(&format!("DROP DATABASE IF EXISTS \"{name}\" WITH (FORCE)"))
        .execute(&admin_pool)
        .await;
}

fn sample_low_code_definition(entity_name: &str) -> LowCodeEntityDefinition {
    LowCodeEntityDefinition {
        name: entity_name.to_string(),
        label: "Orchestrator E2E".to_string(),
        fields: vec![EntityField {
            name: "title".to_string(),
            label: "Title".to_string(),
            kind: FieldKind::String,
            required: Some(true),
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
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

async fn drop_entity_table(pool: &PgPool, entity_name: &str) {
    let table = metap_reconciler::table_name_for(entity_name);
    sqlx::query(&format!(
        "DROP TABLE IF EXISTS \"{}\".\"{table}\" CASCADE",
        metap_reconciler::ENTITY_SCHEMA
    ))
    .execute(pool)
    .await
    .unwrap();
}

async fn cleanup_deployment_row(pool: &PgPool, tenant_id: Uuid, entity_name: &str) {
    sqlx::query("DELETE FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2")
        .bind(tenant_id)
        .bind(entity_name)
        .execute(pool)
        .await
        .unwrap();
}

/// The happy path this whole crate exists for: a published low-code entity, enqueued for one
/// tenant, gets an actual dedicated table via a single `run_once` tick — no code-authored entity
/// registration, no boot-time call, just the queue + the ticker.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn run_once_reconciles_a_claimed_published_entity_into_its_own_table() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = entity_name("orc_e2e_happy");
    drop_entity_table(&pool, &entity_name).await;

    let definition = LowCodeEntityDefinition {
        name: entity_name.clone(),
        label: "Orchestrator E2E".to_string(),
        fields: vec![EntityField {
            name: "title".to_string(),
            label: "Title".to_string(),
            kind: FieldKind::String,
            required: Some(true),
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
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    };
    metap_lowcode::save_draft(&pool, &entity_name, &definition)
        .await
        .unwrap();
    metap_lowcode::publish(&pool, &entity_name, &MetadataRegistry::new())
        .await
        .unwrap();

    enqueue_deployment(&pool, tenant_id, &entity_name, 1).await.unwrap();

    let router = router_for(&pool).await;
    let claimed = run_once(&pool, &router, &config("e2e-happy-path", &entity_name))
        .await
        .unwrap();
    assert_eq!(claimed, 1);

    let (status, applied_version): (String, Option<i64>) = sqlx::query_as(
        "SELECT status, applied_version FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(&entity_name)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(status, "done");
    assert_eq!(applied_version, Some(1));

    let table = metap_reconciler::table_name_for(&entity_name);
    let table_exists: bool = sqlx::query_scalar(
        "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)",
    )
    .bind(metap_reconciler::ENTITY_SCHEMA)
    .bind(&table)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert!(table_exists, "reconcile() must have created the dedicated table");

    // Level-triggered: a second tick against the same (now-satisfied) desired state claims
    // nothing — `claim_due`'s own filter is `desired_version > applied_version`.
    let second = run_once(&pool, &router, &config("e2e-happy-path-2", &entity_name))
        .await
        .unwrap();
    assert_eq!(second, 0, "nothing left to claim once desired == applied");

    drop_entity_table(&pool, &entity_name).await;
    cleanup_deployment_row(&pool, tenant_id, &entity_name).await;
}

/// An entity enqueued but never published has nothing for `reconcile_one` to resolve — this
/// must classify as a failure (recorded via `record_failure`, `claim_due`'s own filter takes it
/// out of automatic rotation) rather than panicking the whole batch, matching §6.4's
/// "một tenant/entity fail KHÔNG chặn cái khác" isolation `run_claimed_batch` already provides.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn run_once_records_failure_for_an_unpublished_entity() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let entity_name = entity_name("orc_e2e_unpub");

    enqueue_deployment(&pool, tenant_id, &entity_name, 1).await.unwrap();

    let router = router_for(&pool).await;
    let claimed = run_once(&pool, &router, &config("e2e-unpublished", &entity_name))
        .await
        .unwrap();
    assert_eq!(
        claimed, 1,
        "claim_due still claims it — the failure happens during reconcile, not claim"
    );

    let status: String = sqlx::query_scalar(
        "SELECT status FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(&entity_name)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(
        status, "blocked",
        "an unresolvable entity is a Fatal-classified error, not a transient one"
    );

    cleanup_deployment_row(&pool, tenant_id, &entity_name).await;
}

/// Nothing pending/failed in the queue — `run_once` must be a cheap no-op, not an error.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn run_once_with_no_due_work_returns_zero() {
    let pool = connect().await;
    let router = router_for(&pool).await;
    // A fresh, never-enqueued entity name — nothing else in a concurrently-running test can
    // ever have a pending row under this name, so the scoped claim below is genuinely empty
    // rather than "empty because another test claimed it first."
    let entity_name = entity_name("orc_e2e_idle");
    let claimed = run_once(&pool, &router, &config("e2e-idle-worker", &entity_name))
        .await
        .unwrap();
    assert_eq!(claimed, 0);
}

/// `run_tick` must reach a `DedicatedDb` tenant's own database, not just the platform's shared
/// one — the fan-out this crate's module doc comment calls out as previously missing. A real
/// second physical database is provisioned (`provision_dedicated_db_tenant` runs every
/// `crates/migrations/*.sql` against it, so it has its own private `reconciler_entity_deployments`
/// and `low_code_entity_versions` tables, same as any real dedicated-db tenant), the entity is
/// published *there*, and the deployment is enqueued *there* — `run_tick`'s only handle on any of
/// this is the shared platform pool's `control.tenants` row plus `Router::pool_for` resolving the
/// dedicated DSN from the env var `EnvStore` reads.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn run_tick_reaches_a_dedicated_db_tenant_own_database() {
    let pool = connect().await;
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let tenant_id = Uuid::new_v4();
    let dsn_secret_ref = format!("METAP_TEST_ORCHESTRATOR_DSN_{}", tenant_id.simple());
    let db_name = format!("test_orc_dedicated_{}", tenant_id.simple());
    let dedicated_url = create_throwaway_database(&database_url, &db_name).await;
    std::env::set_var(&dsn_secret_ref, &dedicated_url);

    let registry = PostgresTenantRegistry::new(pool.clone());
    metap_control::provision_dedicated_db_tenant(
        &registry,
        tenant_id,
        &dsn_secret_ref,
        &dedicated_url,
        &format!("admin-{}@test.local", tenant_id.simple()),
        "pass123",
    )
    .await
    .expect("provision_dedicated_db_tenant");

    let dedicated_pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&dedicated_url)
        .await
        .expect("connect to dedicated database");

    let entity_name = entity_name("orc_e2e_dedic");
    let definition = sample_low_code_definition(&entity_name);
    metap_lowcode::save_draft(&dedicated_pool, &entity_name, &definition)
        .await
        .unwrap();
    metap_lowcode::publish(&dedicated_pool, &entity_name, &MetadataRegistry::new())
        .await
        .unwrap();
    enqueue_deployment(&dedicated_pool, tenant_id, &entity_name, 1)
        .await
        .unwrap();

    let router = router_for(&pool).await;
    let claimed = run_tick(&pool, &router, &registry, &config("e2e-dedicated-fanout", &entity_name))
        .await
        .unwrap();
    assert_eq!(
        claimed, 1,
        "the shared pool has nothing due — this must come from the dedicated tenant's sweep"
    );

    let (status, applied_version): (String, Option<i64>) = sqlx::query_as(
        "SELECT status, applied_version FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(&entity_name)
    .fetch_one(&dedicated_pool)
    .await
    .unwrap();
    assert_eq!(status, "done");
    assert_eq!(applied_version, Some(1));

    let table = metap_reconciler::table_name_for(&entity_name);
    let table_exists: bool = sqlx::query_scalar(
        "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)",
    )
    .bind(metap_reconciler::ENTITY_SCHEMA)
    .bind(&table)
    .fetch_one(&dedicated_pool)
    .await
    .unwrap();
    assert!(
        table_exists,
        "reconcile() must have created the dedicated table on the tenant's OWN database"
    );

    dedicated_pool.close().await;
    drop_throwaway_database(&database_url, &db_name).await;
    std::env::remove_var(&dsn_secret_ref);
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .unwrap();
}
