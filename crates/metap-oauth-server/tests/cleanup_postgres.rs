//! Live-verifies `delete_expired` actually removes stale rows and leaves live ones alone —
//! can't be proven against a mock since the whole point is the `WHERE` clause's interaction with
//! real `expires_at`/`revoked_at` timestamps. `#[ignore]`d, same convention as every other e2e
//! test in this crate (`cargo test -p metap-oauth-server -- --ignored`).

use chrono::{Duration as ChronoDuration, Utc};
use metap_oauth_server::{create_client, delete_expired_tokens, CreateClientInput};
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(4)
        .connect(&database_url)
        .await
        .unwrap()
}

// `oauth_clients.service_user_id` is a real FK into `users` — inserted by hand here since this
// crate has no `metap-auth` dependency to provision one the real way.
async fn make_test_user(pool: &PgPool, tenant_id: Uuid) -> Uuid {
    sqlx::query_scalar("INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, 'test') RETURNING id")
        .bind(tenant_id)
        .bind(format!("{}@example.com", Uuid::new_v4()))
        .fetch_one(pool)
        .await
        .unwrap()
}

async fn make_client(pool: &PgPool, tenant_id: Uuid) -> Uuid {
    let service_user_id = make_test_user(pool, tenant_id).await;
    create_client(
        pool,
        CreateClientInput {
            tenant_id,
            name: "Cleanup Test Client".to_string(),
            redirect_uris: vec!["https://example.com/callback".to_string()],
            allowed_scopes: vec!["read:widgets".to_string()],
            is_confidential: true,
            service_user_id,
        },
    )
    .await
    .unwrap()
    .0
    .id
}

async fn insert_code(pool: &PgPool, client_id: Uuid, tenant_id: Uuid, expires_at: chrono::DateTime<Utc>) -> Uuid {
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

async fn insert_refresh_token(
    pool: &PgPool,
    client_id: Uuid,
    tenant_id: Uuid,
    expires_at: chrono::DateTime<Utc>,
    revoked_at: Option<chrono::DateTime<Utc>>,
) -> Uuid {
    sqlx::query_scalar(
        "INSERT INTO oauth_refresh_tokens (token_hash, client_id, tenant_id, user_id, scope, expires_at, revoked_at) \
         VALUES ($1, $2, $3, $4, '', $5, $6) \
         RETURNING id",
    )
    .bind(Uuid::new_v4().to_string())
    .bind(client_id)
    .bind(tenant_id)
    .bind(Uuid::new_v4())
    .bind(expires_at)
    .bind(revoked_at)
    .fetch_one(pool)
    .await
    .unwrap()
}

async fn row_exists(pool: &PgPool, table: &str, id: Uuid) -> bool {
    sqlx::query_scalar::<_, i64>(&format!("SELECT count(*) FROM {table} WHERE id = $1"))
        .bind(id)
        .fetch_one(pool)
        .await
        .unwrap()
        > 0
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn deletes_only_rows_past_their_grace_period() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let client_id = make_client(&pool, tenant_id).await;
    let now = Utc::now();

    // Well past the 24h grace period on both sides of the sweep's OR clause.
    let long_expired_code = insert_code(&pool, client_id, tenant_id, now - ChronoDuration::days(2)).await;
    let long_expired_token =
        insert_refresh_token(&pool, client_id, tenant_id, now - ChronoDuration::days(2), None).await;
    let long_revoked_token = insert_refresh_token(
        &pool,
        client_id,
        tenant_id,
        now + ChronoDuration::days(10),
        Some(now - ChronoDuration::days(2)),
    )
    .await;

    // Still live — neither expired nor revoked.
    let live_code = insert_code(&pool, client_id, tenant_id, now + ChronoDuration::seconds(60)).await;
    let live_token = insert_refresh_token(&pool, client_id, tenant_id, now + ChronoDuration::days(30), None).await;

    let counts = delete_expired_tokens(&pool).await.unwrap();
    assert!(counts.authorization_codes_deleted >= 1);
    assert!(counts.refresh_tokens_deleted >= 2);

    assert!(!row_exists(&pool, "oauth_authorization_codes", long_expired_code).await);
    assert!(!row_exists(&pool, "oauth_refresh_tokens", long_expired_token).await);
    assert!(!row_exists(&pool, "oauth_refresh_tokens", long_revoked_token).await);

    assert!(row_exists(&pool, "oauth_authorization_codes", live_code).await);
    assert!(row_exists(&pool, "oauth_refresh_tokens", live_token).await);

    // Cleanup this test's own live leftovers so a re-run of the suite starts clean.
    sqlx::query("DELETE FROM oauth_authorization_codes WHERE id = $1")
        .bind(live_code)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM oauth_refresh_tokens WHERE id = $1")
        .bind(live_token)
        .execute(&pool)
        .await
        .ok();
}
