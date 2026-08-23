//! E2E test running `S3ObjectStore` against a real local SeaweedFS S3 gateway
//! (`docker compose up -d seaweedfs`, `docker-compose.yml`'s `seaweedfs` service).
//! `#[ignore]`d — see `crates/metap-query/tests/query_planner_postgres.rs`'s doc comment for the
//! convention this repo already uses for "needs a real backing service" tests.

use bytes::Bytes;
use metap_storage::{ObjectStore, S3ObjectStore, S3ObjectStoreConfig};
use secrecy::SecretString;
use uuid::Uuid;

fn store(bucket: &str) -> S3ObjectStore {
    S3ObjectStore::new(S3ObjectStoreConfig {
        endpoint_url: Some("http://localhost:8333".to_string()),
        region: "us-east-1".to_string(),
        access_key: SecretString::from("anonymous"),
        secret_key: SecretString::from("anonymous"),
        bucket: bucket.to_string(),
        force_path_style: true,
    })
}

async fn ensure_bucket(bucket: &str) {
    let credentials = aws_sdk_s3::config::Credentials::new("anonymous", "anonymous", None, None, "test");
    let config = aws_sdk_s3::config::Builder::new()
        .region(aws_sdk_s3::config::Region::new("us-east-1"))
        .credentials_provider(credentials)
        .endpoint_url("http://localhost:8333")
        .force_path_style(true)
        .behavior_version(aws_sdk_s3::config::BehaviorVersion::latest())
        .build();
    let client = aws_sdk_s3::Client::from_conf(config);
    // Idempotent for this test's purposes — SeaweedFS returns a normal error for
    // create-bucket-that-already-exists, harmless to ignore here since every test run picks a
    // fresh random key anyway (the bucket itself is shared/reused across runs).
    let _ = client.create_bucket().bucket(bucket).send().await;
}

#[tokio::test]
#[ignore = "e2e: requires `docker compose up -d seaweedfs`"]
async fn put_get_delete_round_trip_against_real_seaweedfs() {
    let bucket = "metap-storage-test";
    ensure_bucket(bucket).await;
    let store = store(bucket);
    let tenant_a = Uuid::new_v4();
    let key = "test/file.txt";

    assert!(
        !store.exists(tenant_a, key).await.unwrap(),
        "key must not exist before put"
    );
    assert_eq!(
        store.get(tenant_a, key).await.unwrap(),
        None,
        "get on a missing key must be Ok(None), not an error"
    );

    store
        .put(
            tenant_a,
            key,
            Bytes::from_static(b"hello seaweedfs"),
            Some("text/plain"),
        )
        .await
        .unwrap();

    assert!(store.exists(tenant_a, key).await.unwrap());
    let fetched = store.get(tenant_a, key).await.unwrap().expect("just wrote this key");
    assert_eq!(fetched, Bytes::from_static(b"hello seaweedfs"));

    // Overwrite — same key, different content, must fully replace (no partial/append semantics).
    store
        .put(
            tenant_a,
            key,
            Bytes::from_static(b"updated content"),
            Some("text/plain"),
        )
        .await
        .unwrap();
    let updated = store
        .get(tenant_a, key)
        .await
        .unwrap()
        .expect("still there after overwrite");
    assert_eq!(updated, Bytes::from_static(b"updated content"));

    store.delete(tenant_a, key).await.unwrap();
    assert!(
        !store.exists(tenant_a, key).await.unwrap(),
        "key must be gone after delete"
    );
    assert_eq!(store.get(tenant_a, key).await.unwrap(), None);

    // Deleting an already-missing key must not error (S3 DELETE is idempotent).
    store.delete(tenant_a, key).await.unwrap();
}

/// The actual security property this design exists for: two tenants writing to the *same*
/// logical key must never collide — each tenant's data is scoped by `tenant_id` internally
/// (`S3ObjectStore::scoped_key`), not by caller-supplied prefixing discipline.
#[tokio::test]
#[ignore = "e2e: requires `docker compose up -d seaweedfs`"]
async fn same_key_different_tenants_never_collide() {
    let bucket = "metap-storage-test";
    ensure_bucket(bucket).await;
    let store = store(bucket);
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();
    let key = "shared/name.txt";

    store
        .put(tenant_a, key, Bytes::from_static(b"tenant a's secret"), None)
        .await
        .unwrap();
    store
        .put(tenant_b, key, Bytes::from_static(b"tenant b's secret"), None)
        .await
        .unwrap();

    assert_eq!(
        store.get(tenant_a, key).await.unwrap(),
        Some(Bytes::from_static(b"tenant a's secret"))
    );
    assert_eq!(
        store.get(tenant_b, key).await.unwrap(),
        Some(Bytes::from_static(b"tenant b's secret"))
    );

    store.delete(tenant_a, key).await.unwrap();
    assert!(
        store.exists(tenant_b, key).await.unwrap(),
        "deleting tenant a's object must not touch tenant b's"
    );

    store.delete(tenant_b, key).await.unwrap();
}

/// A traversal-shaped key must never resolve outside its own tenant prefix, no matter what a
/// caller passes — checked before any request reaches the backend.
#[tokio::test]
#[ignore = "e2e: requires `docker compose up -d seaweedfs`"]
async fn traversal_shaped_key_is_rejected_before_it_reaches_the_backend() {
    let bucket = "metap-storage-test";
    ensure_bucket(bucket).await;
    let store = store(bucket);
    let tenant_id = Uuid::new_v4();

    assert!(store
        .put(tenant_id, "../other/secret", Bytes::new(), None)
        .await
        .is_err());
    assert!(store.get(tenant_id, "../other/secret").await.is_err());
    assert!(store.exists(tenant_id, "a/../../escape").await.is_err());
}
