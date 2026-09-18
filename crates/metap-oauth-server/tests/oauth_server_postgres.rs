//! Live-verifies the full authorization_code -> access/refresh token -> rotation ->
//! reuse-detection -> revoke lifecycle against a real Postgres, since none of `client.rs`/
//! `code.rs`/`refresh.rs`'s atomicity guarantees (single-use code, refresh-token rotation, reuse
//! detection) can be proven against a mock. `#[ignore]`d, same convention as every other e2e test
//! in this repo (`cargo test -p metap-oauth-server -- --ignored`).

use metap_oauth_server::{
    consume_authorization_code, consume_refresh_token, create_authorization_code, create_client, create_refresh_token,
    get_client_by_client_id, revoke_client, revoke_refresh_token, verify_client_secret, ConsumeRefreshOutcome,
    CreateClientInput, CreateCodeInput, CreateRefreshInput,
};
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

async fn make_confidential_client(pool: &PgPool, tenant_id: Uuid) -> (metap_oauth_server::OAuthClient, String) {
    create_client(
        pool,
        CreateClientInput {
            tenant_id,
            name: "Test Integration".to_string(),
            redirect_uris: vec!["https://example.com/callback".to_string()],
            allowed_scopes: vec!["read:widgets".to_string(), "write:widgets".to_string()],
            is_confidential: true,
        },
    )
    .await
    .unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn client_secret_is_never_stored_in_plaintext_and_verifies_correctly() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let (client, secret) = make_confidential_client(&pool, tenant_id).await;

    let looked_up = get_client_by_client_id(&pool, &client.client_id)
        .await
        .unwrap()
        .unwrap();
    assert_ne!(
        looked_up.client_secret_hash, secret,
        "the raw secret must never be the stored value"
    );
    assert!(verify_client_secret(&looked_up, &secret));
    assert!(!verify_client_secret(&looked_up, "wrong-secret"));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn revoked_client_fails_secret_verification_even_with_the_right_secret() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let (client, secret) = make_confidential_client(&pool, tenant_id).await;

    assert!(revoke_client(&pool, tenant_id, client.id).await.unwrap());
    let looked_up = get_client_by_client_id(&pool, &client.client_id)
        .await
        .unwrap()
        .unwrap();
    assert!(!verify_client_secret(&looked_up, &secret));
    // A second revoke of an already-revoked client reports "nothing changed", not an error.
    assert!(!revoke_client(&pool, tenant_id, client.id).await.unwrap());
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn authorization_code_is_single_use() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let (client, _secret) = make_confidential_client(&pool, tenant_id).await;
    let redirect_uri = "https://example.com/callback".to_string();

    let (_, raw_code) = create_authorization_code(
        &pool,
        CreateCodeInput {
            client_id: client.id,
            tenant_id,
            user_id,
            redirect_uri: redirect_uri.clone(),
            scope: "read:widgets".to_string(),
            code_challenge: None,
            code_challenge_method: None,
        },
    )
    .await
    .unwrap();

    let first = consume_authorization_code(&pool, &raw_code, client.id, &redirect_uri)
        .await
        .unwrap();
    assert!(first.is_some(), "first redemption must succeed");

    let second = consume_authorization_code(&pool, &raw_code, client.id, &redirect_uri)
        .await
        .unwrap();
    assert!(
        second.is_none(),
        "a second redemption of the same code must fail — this is the replay this test exists to rule out"
    );
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn authorization_code_rejects_a_mismatched_redirect_uri() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let (client, _secret) = make_confidential_client(&pool, tenant_id).await;

    let (_, raw_code) = create_authorization_code(
        &pool,
        CreateCodeInput {
            client_id: client.id,
            tenant_id,
            user_id,
            redirect_uri: "https://example.com/callback".to_string(),
            scope: String::new(),
            code_challenge: None,
            code_challenge_method: None,
        },
    )
    .await
    .unwrap();

    let wrong = consume_authorization_code(&pool, &raw_code, client.id, "https://attacker.example/callback")
        .await
        .unwrap();
    assert!(
        wrong.is_none(),
        "RFC 6749 §4.1.3: redirect_uri must match exactly what /authorize used"
    );

    // The code is still live for the *correct* redirect_uri — a mismatched attempt must not burn it.
    let correct = consume_authorization_code(&pool, &raw_code, client.id, "https://example.com/callback")
        .await
        .unwrap();
    assert!(correct.is_some());
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn refresh_token_rotates_and_the_old_one_stops_working() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let (client, _secret) = make_confidential_client(&pool, tenant_id).await;

    let mut tx = pool.begin().await.unwrap();
    let (_, raw_refresh) = create_refresh_token(
        &mut tx,
        CreateRefreshInput {
            client_id: client.id,
            tenant_id,
            user_id,
            scope: "read:widgets".to_string(),
        },
    )
    .await
    .unwrap();
    tx.commit().await.unwrap();

    let mut tx = pool.begin().await.unwrap();
    let outcome = consume_refresh_token(&mut tx, &raw_refresh, client.id).await.unwrap();
    tx.commit().await.unwrap();
    let ConsumeRefreshOutcome::Rotated(new_record, raw_new_refresh) = outcome else {
        panic!("expected Rotated");
    };
    assert_eq!(new_record.scope, "read:widgets");
    assert_ne!(raw_new_refresh, raw_refresh);

    // The old token is now dead — confirmed via Reused below, not Invalid, since it was consumed
    // (not merely expired/never-existed).
    let mut tx = pool.begin().await.unwrap();
    let replay = consume_refresh_token(&mut tx, &raw_refresh, client.id).await.unwrap();
    tx.commit().await.unwrap();
    assert!(matches!(replay, ConsumeRefreshOutcome::Reused));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn refresh_token_reuse_revokes_the_whole_chain() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let (client, _secret) = make_confidential_client(&pool, tenant_id).await;

    let mut tx = pool.begin().await.unwrap();
    let (_, raw_r0) = create_refresh_token(
        &mut tx,
        CreateRefreshInput {
            client_id: client.id,
            tenant_id,
            user_id,
            scope: String::new(),
        },
    )
    .await
    .unwrap();
    tx.commit().await.unwrap();

    // Legitimate rotation: r0 -> r1.
    let mut tx = pool.begin().await.unwrap();
    let ConsumeRefreshOutcome::Rotated(_, raw_r1) = consume_refresh_token(&mut tx, &raw_r0, client.id).await.unwrap()
    else {
        panic!("expected Rotated");
    };
    tx.commit().await.unwrap();

    // An attacker who captured r0 replays it — this must revoke r1 too (the chain), even though
    // r1 was never itself presented improperly.
    let mut tx = pool.begin().await.unwrap();
    let replay = consume_refresh_token(&mut tx, &raw_r0, client.id).await.unwrap();
    tx.commit().await.unwrap();
    assert!(matches!(replay, ConsumeRefreshOutcome::Reused));

    // The legitimate holder's r1 (obtained before the replay) is now unusable too.
    let mut tx = pool.begin().await.unwrap();
    let legit_after_reuse = consume_refresh_token(&mut tx, &raw_r1, client.id).await.unwrap();
    tx.commit().await.unwrap();
    assert!(
        matches!(
            legit_after_reuse,
            ConsumeRefreshOutcome::Invalid | ConsumeRefreshOutcome::Reused
        ),
        "r1 must be dead once reuse was detected on its predecessor"
    );
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn revoke_refresh_token_stops_it_from_rotating() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let (client, _secret) = make_confidential_client(&pool, tenant_id).await;

    let mut tx = pool.begin().await.unwrap();
    let (_, raw_refresh) = create_refresh_token(
        &mut tx,
        CreateRefreshInput {
            client_id: client.id,
            tenant_id,
            user_id,
            scope: String::new(),
        },
    )
    .await
    .unwrap();
    tx.commit().await.unwrap();

    revoke_refresh_token(&pool, &raw_refresh, client.id).await.unwrap();

    let mut tx = pool.begin().await.unwrap();
    let outcome = consume_refresh_token(&mut tx, &raw_refresh, client.id).await.unwrap();
    tx.commit().await.unwrap();
    assert!(
        matches!(outcome, ConsumeRefreshOutcome::Reused),
        "an explicitly revoked token must read back as already-consumed"
    );
}
