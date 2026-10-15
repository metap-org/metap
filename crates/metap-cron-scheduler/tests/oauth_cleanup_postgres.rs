//! Live-verifies `oauth_cleanup::run` really starts the sweep: the first pass runs immediately
//! (before the first interval elapses), removes a long-expired authorization code and leaves a
//! live one alone. `metap-oauth-server`'s own `cleanup_postgres.rs` covers the `WHERE` clauses in
//! depth; this only proves the wiring. `#[ignore]`d like every e2e test here
//! (`cargo test -p metap-cron-scheduler -- --ignored`, needs `DATABASE_URL`).

use std::time::Duration;

use chrono::{DateTime, Duration as ChronoDuration, Utc};
use metap_oauth_server::{create_client, CreateClientInput};
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

async fn insert_code(pool: &PgPool, client_id: Uuid, tenant_id: Uuid, expires_at: DateTime<Utc>) -> Uuid {
    sqlx::query_scalar(
        "INSERT INTO oauth_authorization_codes \
            (code_hash, client_id, tenant_id, user_id, redirect_uri, scope, expires_at) \
         VALUES ($1, $2, $3, $4, 'https://example.com/callback', '', $5) \
         RETURNING id",
    )
    .bind(Uuid::new_v4().to_string())
    .bind(client_id)
    .bind(tenant_id)
    .bind(Uuid::new_v4())
    .bind(expires_at)
    .fetch_one(pool)
    .await
    .unwrap()
}

async fn code_exists(pool: &PgPool, id: Uuid) -> bool {
    sqlx::query_scalar::<_, i64>("SELECT count(*) FROM oauth_authorization_codes WHERE id = $1")
        .bind(id)
        .fetch_one(pool)
        .await
        .unwrap()
        > 0
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn first_pass_runs_immediately_and_sweeps_only_stale_rows() {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(4)
        .connect(&database_url)
        .await
        .unwrap();

    let tenant_id = Uuid::new_v4();
    // `oauth_clients.service_user_id` is a real FK into `users`.
    let service_user_id: Uuid =
        sqlx::query_scalar("INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, 'test') RETURNING id")
            .bind(tenant_id)
            .bind(format!("{}@example.com", Uuid::new_v4()))
            .fetch_one(&pool)
            .await
            .unwrap();
    let client_id = create_client(
        &pool,
        CreateClientInput {
            tenant_id,
            name: "Cron Cleanup Wiring Test".to_string(),
            redirect_uris: vec!["https://example.com/callback".to_string()],
            allowed_scopes: vec!["read:widgets".to_string()],
            is_confidential: true,
            service_user_id,
        },
    )
    .await
    .unwrap()
    .0
    .id;

    let now = Utc::now();
    let stale = insert_code(&pool, client_id, tenant_id, now - ChronoDuration::days(2)).await;
    let live = insert_code(&pool, client_id, tenant_id, now + ChronoDuration::minutes(5)).await;

    // Interval far longer than the test: a deleted row proves the sweep ran on startup rather
    // than after one tick. Shutdown fires shortly after, so `run` returns instead of looping.
    cron_scheduler::oauth_cleanup::run(
        pool.clone(),
        Duration::from_secs(3600),
        tokio::time::sleep(Duration::from_millis(500)),
    )
    .await;

    assert!(!code_exists(&pool, stale).await, "long-expired code must be swept");
    assert!(code_exists(&pool, live).await, "live code must survive");

    sqlx::query("DELETE FROM oauth_authorization_codes WHERE client_id = $1")
        .bind(client_id)
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("DELETE FROM oauth_clients WHERE id = $1")
        .bind(client_id)
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("DELETE FROM users WHERE id = $1")
        .bind(service_user_id)
        .execute(&pool)
        .await
        .unwrap();
}
