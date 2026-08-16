//! E2E test against the repo's real dev Postgres (see `CLAUDE.md`'s Commands section:
//! `docker compose up -d postgres`), same convention as
//! `crates/metap-permission/tests/postgres_policy_store.rs`: `#[ignore]`d so a plain `cargo
//! test` never touches a database, run explicitly with `cargo test -- --ignored`. Entity
//! names are randomized per test (metadata here is global, not tenant-scoped, so there's no
//! `tenant_id` to isolate on the way other e2e tests do).

use metap_lowcode::{LowCodeEntityDefinition, PublishError};
use metap_metadata::{EntityField, FieldKind, MetadataRegistry};
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

async fn pool() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(2)
        .connect(&database_url)
        .await
        .expect("connect to dev postgres")
}

fn field(name: &str, kind: FieldKind) -> EntityField {
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
    }
}

fn definition(name: &str) -> LowCodeEntityDefinition {
    LowCodeEntityDefinition {
        name: name.to_string(),
        label: "Test Entity".to_string(),
        fields: vec![field("name", FieldKind::String)],
        list_views: vec![],
    }
}

fn entity_name(prefix: &str) -> String {
    format!("test.{prefix}_{}", Uuid::new_v4().simple())
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn save_draft_then_get_draft_round_trips() {
    let pool = pool().await;
    let name = entity_name("draft_roundtrip");
    let def = definition(&name);

    metap_lowcode::save_draft(&pool, &name, &def).await.expect("save_draft");
    let loaded = metap_lowcode::get_draft(&pool, &name).await.expect("get_draft");

    assert_eq!(loaded.unwrap().label, "Test Entity");
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn save_draft_rejects_invalid_shape() {
    let pool = pool().await;
    let name = entity_name("invalid_shape");
    let mut def = definition(&name);
    def.fields.push(def.fields[0].clone()); // duplicate field name

    let err = metap_lowcode::save_draft(&pool, &name, &def).await.unwrap_err();
    assert!(matches!(err, PublishError::Invalid(_)));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn publish_without_draft_fails() {
    let pool = pool().await;
    let name = entity_name("publish_no_draft");
    let registry = MetadataRegistry::new();

    let err = metap_lowcode::publish(&pool, &name, &registry).await.unwrap_err();
    assert!(matches!(err, PublishError::NoDraft));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn publish_rejects_name_reserved_by_code_authored_entity() {
    let pool = pool().await;
    let name = entity_name("reserved");
    let def = definition(&name);
    metap_lowcode::save_draft(&pool, &name, &def).await.expect("save_draft");

    let mut registry = MetadataRegistry::new();
    registry
        .register(def.to_entity_definition())
        .expect("register code-authored stand-in");

    let err = metap_lowcode::publish(&pool, &name, &registry).await.unwrap_err();
    assert!(matches!(err, PublishError::NameReservedByCodeEntity));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn publish_rejects_dangling_reference() {
    let pool = pool().await;
    let name = entity_name("dangling_ref");
    let mut def = definition(&name);
    let mut ref_field = field("parent", FieldKind::Reference);
    ref_field.ref_entity = Some("test.does_not_exist".to_string());
    def.fields.push(ref_field);
    metap_lowcode::save_draft(&pool, &name, &def).await.expect("save_draft");

    let registry = MetadataRegistry::new();
    let err = metap_lowcode::publish(&pool, &name, &registry).await.unwrap_err();
    assert!(matches!(err, PublishError::Invalid(_)));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn publish_increments_version_and_get_published_returns_latest() {
    let pool = pool().await;
    let name = entity_name("versioning");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft v1");
    let v1 = metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");
    assert_eq!(v1.version_number, 1);

    let mut v2_def = definition(&name);
    v2_def.label = "Renamed".to_string();
    metap_lowcode::save_draft(&pool, &name, &v2_def)
        .await
        .expect("save_draft v2");
    let v2 = metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v2");
    assert_eq!(v2.version_number, 2);

    let published = metap_lowcode::get_published(&pool, &name)
        .await
        .expect("get_published")
        .unwrap();
    assert_eq!(published.version_number, 2);
    assert_eq!(published.definition.label, "Renamed");
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_versions_returns_newest_first() {
    let pool = pool().await;
    let name = entity_name("list_versions");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft v1");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");
    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft v2");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v2");

    let versions = metap_lowcode::list_versions(&pool, &name).await.expect("list_versions");
    assert_eq!(versions.len(), 2);
    assert_eq!(versions[0].version_number, 2);
    assert_eq!(versions[1].version_number, 1);
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn rollback_to_unknown_version_fails() {
    let pool = pool().await;
    let name = entity_name("rollback_unknown");
    let registry = MetadataRegistry::new();
    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft");
    metap_lowcode::publish(&pool, &name, &registry).await.expect("publish");

    let err = metap_lowcode::rollback(&pool, &name, 99, &registry).await.unwrap_err();
    assert!(matches!(err, PublishError::VersionNotFound(99)));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn rollback_creates_new_version_with_restored_from_set() {
    let pool = pool().await;
    let name = entity_name("rollback_ok");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft v1");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");
    let mut v2_def = definition(&name);
    v2_def.label = "Changed".to_string();
    metap_lowcode::save_draft(&pool, &name, &v2_def)
        .await
        .expect("save_draft v2");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v2");

    let rolled_back = metap_lowcode::rollback(&pool, &name, 1, &registry)
        .await
        .expect("rollback");
    assert_eq!(rolled_back.version_number, 3);

    let versions = metap_lowcode::list_versions(&pool, &name).await.expect("list_versions");
    assert_eq!(versions[0].version_number, 3);
    assert_eq!(versions[0].restored_from_version, Some(1));

    let draft = metap_lowcode::get_draft(&pool, &name)
        .await
        .expect("get_draft")
        .unwrap();
    assert_eq!(draft.label, "Test Entity"); // v1's label, not "Changed"
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_all_published_includes_every_published_entity_at_latest_version() {
    let pool = pool().await;
    let name_a = entity_name("list_all_a");
    let name_b = entity_name("list_all_b");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name_a, &definition(&name_a))
        .await
        .expect("save_draft a");
    metap_lowcode::publish(&pool, &name_a, &registry)
        .await
        .expect("publish a");
    metap_lowcode::save_draft(&pool, &name_b, &definition(&name_b))
        .await
        .expect("save_draft b");
    metap_lowcode::publish(&pool, &name_b, &registry)
        .await
        .expect("publish b");

    let all = metap_lowcode::list_all_published(&pool)
        .await
        .expect("list_all_published");
    let names: Vec<&str> = all.iter().map(|(n, _)| n.as_str()).collect();
    assert!(names.contains(&name_a.as_str()));
    assert!(names.contains(&name_b.as_str()));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn concurrent_publishes_of_the_same_entity_serialize_instead_of_colliding() {
    // Regression test for the version-number race: without the advisory-lock transaction in
    // `insert_version`, two concurrent publishes can both read the same `MAX(version_number)`
    // and both try to insert it, tripping the unique constraint and surfacing as a generic
    // `PublishError::Db` instead of both succeeding with distinct sequential versions.
    let pool_a = pool().await;
    let pool_b = pool().await;
    let name = entity_name("concurrent_publish");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool_a, &name, &definition(&name))
        .await
        .expect("save_draft");

    let (result_a, result_b) = tokio::join!(
        metap_lowcode::publish(&pool_a, &name, &registry),
        metap_lowcode::publish(&pool_b, &name, &registry)
    );

    let mut versions: Vec<i32> = [result_a, result_b]
        .into_iter()
        .map(|r| r.expect("publish").version_number)
        .collect();
    versions.sort();
    assert_eq!(versions, vec![1, 2]);
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn disabling_an_entity_excludes_it_from_enabled_published_but_not_all_published() {
    let pool = pool().await;
    let name = entity_name("disable_toggle");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft");
    metap_lowcode::publish(&pool, &name, &registry).await.expect("publish");

    let enabled_names: Vec<String> = metap_lowcode::list_enabled_published(&pool)
        .await
        .expect("list_enabled_published")
        .into_iter()
        .map(|(n, _)| n)
        .collect();
    assert!(enabled_names.contains(&name));

    metap_lowcode::set_enabled(&pool, &name, false)
        .await
        .expect("set_enabled false");

    let enabled_names: Vec<String> = metap_lowcode::list_enabled_published(&pool)
        .await
        .expect("list_enabled_published")
        .into_iter()
        .map(|(n, _)| n)
        .collect();
    assert!(!enabled_names.contains(&name));

    let all_names: Vec<String> = metap_lowcode::list_all_published(&pool)
        .await
        .expect("list_all_published")
        .into_iter()
        .map(|(n, _)| n)
        .collect();
    assert!(all_names.contains(&name), "disabling must not remove publish history");

    metap_lowcode::set_enabled(&pool, &name, true)
        .await
        .expect("set_enabled true");
    let enabled_names: Vec<String> = metap_lowcode::list_enabled_published(&pool)
        .await
        .expect("list_enabled_published")
        .into_iter()
        .map(|(n, _)| n)
        .collect();
    assert!(enabled_names.contains(&name), "re-enabling must bring it back");
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn publishing_a_disabled_entity_does_not_implicitly_reenable_it_in_the_live_registry() {
    let pool = pool().await;
    let name = entity_name("disabled_publish");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft v1");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");
    metap_lowcode::set_enabled(&pool, &name, false)
        .await
        .expect("set_enabled false");

    let mut v2_def = definition(&name);
    v2_def.label = "Still disabled".to_string();
    metap_lowcode::save_draft(&pool, &name, &v2_def)
        .await
        .expect("save_draft v2");
    let outcome = metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v2");

    assert!(
        outcome.registry.get_entity(&name).is_none(),
        "publishing a disabled entity must not resurrect it in the swapped-live registry"
    );
}
