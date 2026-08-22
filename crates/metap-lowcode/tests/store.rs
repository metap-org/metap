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
        workflow: None,
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
async fn preview_publish_reports_the_would_be_version_without_writing_anything() {
    // Phase B publish preview/validation report (`docs/roadmap.md`, 2026-08-17) — must run the
    // exact same checks `publish` does, but leave no trace: no new version row, no change to
    // what `get_published` sees.
    let pool = pool().await;
    let name = entity_name("preview");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft");
    let preview = metap_lowcode::preview_publish(&pool, &name, &registry)
        .await
        .expect("preview_publish");
    assert_eq!(preview.would_be_version, 1);
    assert!(
        metap_lowcode::list_versions(&pool, &name)
            .await
            .expect("list_versions")
            .is_empty(),
        "preview must not insert a version row"
    );

    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");
    let mut v2_def = definition(&name);
    v2_def.label = "Changed".to_string();
    metap_lowcode::save_draft(&pool, &name, &v2_def)
        .await
        .expect("save_draft v2");
    let preview2 = metap_lowcode::preview_publish(&pool, &name, &registry)
        .await
        .expect("preview_publish after v1");
    assert_eq!(preview2.would_be_version, 2);
    assert_eq!(
        metap_lowcode::get_published(&pool, &name)
            .await
            .expect("get_published")
            .unwrap()
            .version_number,
        1,
        "preview after publish must not have bumped the live published version"
    );
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn preview_publish_reports_migration_impact_against_the_currently_published_definition() {
    use metap_lowcode::ImpactKind;

    let pool = pool().await;
    let name = entity_name("impact");
    let registry = MetadataRegistry::new();

    let mut status_field = field("status", FieldKind::Enum);
    status_field.enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
    let mut v1_def = definition(&name);
    v1_def.fields = vec![
        field("name", FieldKind::String),
        status_field,
        field("category", FieldKind::String),
    ];
    metap_lowcode::save_draft(&pool, &name, &v1_def)
        .await
        .expect("save_draft v1");
    metap_lowcode::publish(&pool, &name, &registry)
        .await
        .expect("publish v1");

    // v2: drop "category" entirely, narrow "status"'s allowed values, and newly require+unique
    // "name" — one instance of every `ImpactKind` this module knows about.
    let mut status_field_v2 = field("status", FieldKind::Enum);
    status_field_v2.enum_values = Some(vec!["active".to_string()]);
    let mut name_field_v2 = field("name", FieldKind::String);
    name_field_v2.required = Some(true);
    name_field_v2.unique = Some(true);
    let mut v2_def = definition(&name);
    v2_def.fields = vec![name_field_v2, status_field_v2];
    metap_lowcode::save_draft(&pool, &name, &v2_def)
        .await
        .expect("save_draft v2");

    let preview = metap_lowcode::preview_publish(&pool, &name, &registry)
        .await
        .expect("preview_publish");

    let kinds: Vec<ImpactKind> = preview.impact.iter().map(|w| w.kind).collect();
    assert!(kinds.contains(&ImpactKind::FieldRemoved), "{kinds:?}");
    assert!(kinds.contains(&ImpactKind::EnumValueRemoved), "{kinds:?}");
    assert!(kinds.contains(&ImpactKind::FieldMadeRequired), "{kinds:?}");
    assert!(kinds.contains(&ImpactKind::FieldMadeUnique), "{kinds:?}");
    assert_eq!(
        preview.impact.len(),
        4,
        "no extra/duplicate warnings expected: {:#?}",
        preview.impact
    );
    assert!(preview.impact.iter().any(|w| w.field == "category"));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn preview_publish_surfaces_the_same_errors_publish_would() {
    let pool = pool().await;

    let no_draft_name = entity_name("preview_no_draft");
    let registry = MetadataRegistry::new();
    let err = metap_lowcode::preview_publish(&pool, &no_draft_name, &registry)
        .await
        .unwrap_err();
    assert!(matches!(err, PublishError::NoDraft));

    // `save_draft` itself already rejects shape-invalid drafts (see
    // `save_draft_rejects_invalid_shape`), so the only way to get an *invalid* draft actually
    // persisted is the same gap `publish_rejects_dangling_reference` exercises: a dangling
    // `refEntity` only fails `validate_references()`, which needs a registry — checked at
    // publish/preview time, not at save_draft time.
    let dangling_name = entity_name("preview_dangling_ref");
    let mut dangling_def = definition(&dangling_name);
    let mut ref_field = field("parent", FieldKind::Reference);
    ref_field.ref_entity = Some("test.does_not_exist".to_string());
    dangling_def.fields.push(ref_field);
    metap_lowcode::save_draft(&pool, &dangling_name, &dangling_def)
        .await
        .expect("save_draft");
    let err = metap_lowcode::preview_publish(&pool, &dangling_name, &registry)
        .await
        .unwrap_err();
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

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn workflow_with_guard_round_trips_through_draft_and_publish() {
    // Phase B (`docs/roadmap.md`, 2026-08-17): DB-authored entities can now carry a workflow,
    // guard included — regression test for the `WorkflowTransition::guard` `#[serde(skip)]`
    // removal (`metap-metadata/src/entity.rs`), which previously would have silently dropped
    // the guard on every save/load through this jsonb-backed store.
    use metap_metadata::{EntityWorkflow, WorkflowTransition};
    use metap_permission::{ConditionOp, PolicyCondition, PolicyValue};

    let pool = pool().await;
    let name = entity_name("workflow_guard");
    let mut def = definition(&name);
    def.fields.push(field("email", FieldKind::String));
    def.fields.push(field("status", FieldKind::Enum));
    def.fields[2].enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
    def.workflow = Some(EntityWorkflow {
        state_field: "status".to_string(),
        initial_state: "draft".to_string(),
        terminal_states: vec![],
        transitions: vec![WorkflowTransition {
            action: "activate".to_string(),
            from: "draft".to_string(),
            to: "active".to_string(),
            label: "Activate".to_string(),
            guard: Some(PolicyCondition::Attribute {
                attribute: "email".to_string(),
                op: ConditionOp::Neq,
                value: PolicyValue::Literal {
                    literal: serde_json::json!(""),
                },
            }),
        }],
    });

    metap_lowcode::save_draft(&pool, &name, &def).await.expect("save_draft");
    let loaded = metap_lowcode::get_draft(&pool, &name)
        .await
        .expect("get_draft")
        .unwrap();
    let loaded_guard = &loaded.workflow.as_ref().unwrap().transitions[0].guard;
    assert!(
        matches!(loaded_guard, Some(PolicyCondition::Attribute { attribute, .. }) if attribute == "email"),
        "guard must round-trip through the jsonb draft column, not be silently dropped"
    );

    let registry = MetadataRegistry::new();
    let outcome = metap_lowcode::publish(&pool, &name, &registry).await.expect("publish");
    let published_entity = outcome
        .registry
        .get_entity(&name)
        .expect("published entity in registry");
    assert!(
        published_entity.workflow.is_some(),
        "publish must carry workflow into the live registry"
    );
    assert!(
        published_entity.workflow.as_ref().unwrap().transitions[0]
            .guard
            .is_some(),
        "publish must carry the guard, not just the workflow shape"
    );
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn export_entities_with_no_filter_returns_every_published_entity() {
    let pool = pool().await;
    let name_a = entity_name("export_all_a");
    let name_b = entity_name("export_all_b");
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

    let exported = metap_lowcode::export_entities(&pool, None)
        .await
        .expect("export_entities");
    let names: Vec<&str> = exported.iter().map(|(n, _)| n.as_str()).collect();
    assert!(names.contains(&name_a.as_str()));
    assert!(names.contains(&name_b.as_str()));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn export_entities_with_a_filter_returns_only_the_requested_names() {
    let pool = pool().await;
    let name_a = entity_name("export_filter_a");
    let name_b = entity_name("export_filter_b");
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

    let exported = metap_lowcode::export_entities(&pool, Some(std::slice::from_ref(&name_a)))
        .await
        .expect("export_entities");
    assert_eq!(exported.len(), 1);
    assert_eq!(exported[0].0, name_a);
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn export_entities_includes_disabled_entities() {
    let pool = pool().await;
    let name = entity_name("export_disabled");
    let registry = MetadataRegistry::new();

    metap_lowcode::save_draft(&pool, &name, &definition(&name))
        .await
        .expect("save_draft");
    metap_lowcode::publish(&pool, &name, &registry).await.expect("publish");
    metap_lowcode::set_enabled(&pool, &name, false)
        .await
        .expect("set_enabled");

    let exported = metap_lowcode::export_entities(&pool, None)
        .await
        .expect("export_entities");
    let names: Vec<&str> = exported.iter().map(|(n, _)| n.as_str()).collect();
    assert!(
        names.contains(&name.as_str()),
        "a disabled-but-published entity must still appear in an export snapshot"
    );
}

/// Phase 11C's "operational visibility" — `audit::list_recent` (unlike `list_for_entity`) isn't
/// scoped to one entity, so unlike every other test in this file it can't isolate on a random
/// entity name alone: the audit table is global (`docs/roadmap.md`'s note on why entity names
/// here are randomized doesn't cover this). Asserts relative ordering between this test's own
/// events instead of asserting exact positions in the full result, since other tests/processes
/// may be writing to the same table concurrently.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_recent_spans_entities_newest_first_and_respects_limit() {
    let pool = pool().await;
    let entity_a = entity_name("audit_a");
    let entity_b = entity_name("audit_b");
    let actor = metap_lowcode::audit::AuditActor {
        user_id: Some(Uuid::new_v4().to_string()),
        tenant_id: Uuid::new_v4().to_string(),
    };

    metap_lowcode::audit::record(
        &pool,
        &entity_a,
        metap_lowcode::audit::AuditAction::DraftSaved,
        &actor,
        Default::default(),
    )
    .await;
    tokio::time::sleep(std::time::Duration::from_millis(5)).await;
    metap_lowcode::audit::record(
        &pool,
        &entity_b,
        metap_lowcode::audit::AuditAction::DraftSaved,
        &actor,
        Default::default(),
    )
    .await;
    tokio::time::sleep(std::time::Duration::from_millis(5)).await;
    metap_lowcode::audit::record(
        &pool,
        &entity_a,
        metap_lowcode::audit::AuditAction::Published,
        &actor,
        metap_lowcode::audit::AuditVersionInfo {
            version_number: Some(1),
            restored_from_version: None,
        },
    )
    .await;

    // `limit` bounds the query regardless of how many events already exist in the (global,
    // not tenant-scoped) audit table.
    let limited = metap_lowcode::audit::list_recent(&pool, 1).await.expect("list_recent");
    assert_eq!(limited.len(), 1);

    let recent = metap_lowcode::audit::list_recent(&pool, 500)
        .await
        .expect("list_recent");
    let a_published_pos = recent
        .iter()
        .position(|e| e.entity_name == entity_a && e.action == "published")
        .expect("entity_a's published event should be present");
    let a_draft_pos = recent
        .iter()
        .position(|e| e.entity_name == entity_a && e.action == "draft_saved")
        .expect("entity_a's draft_saved event should be present");
    let b_draft_pos = recent
        .iter()
        .position(|e| e.entity_name == entity_b && e.action == "draft_saved")
        .expect("entity_b's draft_saved event should be present");
    assert!(
        a_published_pos < b_draft_pos,
        "the most recently recorded event (entity_a published) must sort before an older one"
    );
    assert!(
        b_draft_pos < a_draft_pos,
        "entity_b's draft (recorded after entity_a's) must sort before entity_a's, older, draft"
    );

    // entity-scoped listing is unaffected — still only sees its own entity's events.
    let scoped = metap_lowcode::audit::list_for_entity(&pool, &entity_a)
        .await
        .expect("list_for_entity");
    assert_eq!(scoped.len(), 2);
    assert!(scoped.iter().all(|e| e.entity_name == entity_a));
}
