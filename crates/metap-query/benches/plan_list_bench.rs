//! Micro-benchmark for `plan_list` — the SQL-generation path only (no DB round trip; that's
//! what `testing/performance/baseline.md`'s sustained-load tests already measure against a real
//! Postgres). This tracks whether `plan_list`/`jql`'s pure CPU cost (parsing, validating,
//! building the `WHERE`/`ORDER BY` strings) regresses as the entity/query shape grows, run
//! nightly by `testing/performance/run-nightly-benchmark.sh` — see that script's doc comment for
//! why a full 10M-row benchmark stays a manual, not automated, exercise.

use std::hint::black_box;

use async_trait::async_trait;
use criterion::{criterion_group, criterion_main, Criterion};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::{
    ExplainOptions, PermissionService, PolicyCondition, PolicyEffect, PolicyRow, PolicyStore, PolicySubject,
    RequestContext,
};
use metap_query::{plan_list, ListInput};
use uuid::Uuid;

/// `plan_list` never touches the store (see this crate's doc comment on `PermissionService::
/// scoped_tenant` — pure `Uuid::parse_str`) — every method here is dead code for this benchmark,
/// only present to satisfy `PermissionService::new`'s constructor.
struct UnusedPolicyStore;

#[async_trait]
impl PolicyStore for UnusedPolicyStore {
    async fn find_context_policies(&self, _: Uuid, _: &str, _: &str) -> anyhow::Result<Vec<PolicyRow>> {
        unimplemented!()
    }
    async fn load_all_policies(&self, _: Uuid, _: &str) -> anyhow::Result<Vec<PolicyRow>> {
        unimplemented!()
    }
    async fn find_explain_policies(
        &self,
        _: Uuid,
        _: &str,
        _: &str,
        _: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        unimplemented!()
    }
    async fn list_policies(&self, _: Uuid, _: Option<&str>) -> anyhow::Result<Vec<PolicyRow>> {
        unimplemented!()
    }
    #[allow(clippy::too_many_arguments)]
    async fn create_policy(
        &self,
        _: Uuid,
        _: &str,
        _: &str,
        _: Option<Vec<String>>,
        _: Option<PolicyCondition>,
        _: Option<Uuid>,
        _: Option<&str>,
        _: Option<PolicySubject>,
        _: PolicyEffect,
    ) -> anyhow::Result<PolicyRow> {
        unimplemented!()
    }
    async fn delete_policy(&self, _: Uuid, _: Uuid) -> anyhow::Result<()> {
        unimplemented!()
    }
}

fn field(name: &str, kind: FieldKind, indexed: bool, sortable: bool, searchable: bool) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind,
        required: None,
        indexed: indexed.then_some(true),
        unique: None,
        enum_values: matches!(kind, FieldKind::Enum).then(|| vec!["a".to_string(), "b".to_string(), "c".to_string()]),
        ref_entity: matches!(kind, FieldKind::Reference).then(|| "bench.other".to_string()),
        ref_display_field: None,
        searchable: searchable.then_some(true),
        search_mode: None,
        sortable: sortable.then_some(true),
        storage: None,
    }
}

fn bench_entity() -> EntityDefinition {
    EntityDefinition {
        name: "bench.issues".to_string(),
        label: "Bench Issue".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("title", FieldKind::String, false, false, true),
            field("status", FieldKind::Enum, true, true, false),
            field("priority", FieldKind::Enum, true, true, false),
            field("project", FieldKind::Reference, true, false, false),
            field("storyPoints", FieldKind::Number, false, true, false),
            field("dueDate", FieldKind::Date, false, true, false),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string(), "status".to_string(), "priority".to_string()],
            filters: vec!["status".to_string(), "priority".to_string(), "project".to_string()],
            default_sort: Some("-status".to_string()),
            max_limit: 100,
        }],
        workflow: None,
    }
}

fn bench_registry() -> MetadataRegistry {
    let mut registry = MetadataRegistry::new();
    registry.register(bench_entity()).expect("valid entity");
    registry
}

fn bench_context() -> RequestContext {
    RequestContext {
        tenant_id: Uuid::new_v4().to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["admin".to_string()]),
        function_id: None,
        context_attributes: None,
    }
}

fn bench(c: &mut Criterion) {
    let registry = bench_registry();
    let permissions = PermissionService::new(Box::new(UnusedPolicyStore));
    let context = bench_context();

    let simple_input = ListInput {
        limit: 30,
        sort: None,
        filters: vec![("status".to_string(), "in_progress".to_string())],
        cursor: None,
        list_view: None,
        jql: None,
    };

    let jql_input = ListInput {
        limit: 100,
        sort: None,
        filters: vec![],
        cursor: None,
        list_view: None,
        jql: Some(
            r#"(priority = "high" OR priority = "urgent") AND status != "done" AND storyPoints >= 3 ORDER BY dueDate DESC"#
                .to_string(),
        ),
    };

    c.bench_function("plan_list/simple_filter", |b| {
        b.iter(|| {
            let planned = plan_list(
                black_box(&registry),
                black_box(&permissions),
                black_box("bench.issues"),
                black_box(&simple_input),
                black_box(&context),
                black_box(&[]),
            )
            .unwrap();
            black_box(planned);
        });
    });

    c.bench_function("plan_list/jql_and_or_range_order", |b| {
        b.iter(|| {
            let planned = plan_list(
                black_box(&registry),
                black_box(&permissions),
                black_box("bench.issues"),
                black_box(&jql_input),
                black_box(&context),
                black_box(&[]),
            )
            .unwrap();
            black_box(planned);
        });
    });
}

criterion_group!(benches, bench);
criterion_main!(benches);
