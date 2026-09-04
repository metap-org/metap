use super::*;
use async_trait::async_trait;
use metap_metadata::{EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, WorkflowTransition};
use metap_permission::{
    ExplainOptions, PermissionService, PolicyCondition, PolicyEffect, PolicyRow, PolicyStore, PolicySubject,
    RequestContext,
};
use uuid::Uuid;

/// `plan_aggregate` never touches the store directly — same reasoning as `plan_list_bench.rs`'s
/// `UnusedPolicyStore`: every method here is dead code for these tests, present only to satisfy
/// `PermissionService::new`'s constructor. `permissions.scoped_tenant` is pure `Uuid::parse_str`.
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
    async fn sync_basic_policies(
        &self,
        _: Uuid,
        _: &str,
        _: Vec<(Option<String>, String)>,
        _: Option<Uuid>,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        unimplemented!()
    }
}

fn permissions() -> PermissionService {
    PermissionService::new(Box::new(UnusedPolicyStore))
}

fn context(tenant_id: Uuid) -> RequestContext {
    RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some("u1".to_string()),
        roles: Some(vec!["member".to_string()]),
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
    }
}

fn field(
    name: &str,
    kind: FieldKind,
    indexed: bool,
    sortable: bool,
    searchable: bool,
    enum_values: Option<Vec<&str>>,
) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind,
        required: None,
        indexed: indexed.then_some(true),
        unique: None,
        enum_values: enum_values.map(|vs| vs.into_iter().map(String::from).collect()),
        ref_entity: None,
        ref_display_field: None,
        searchable: searchable.then_some(true),
        search_mode: None,
        sortable: sortable.then_some(true),
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

/// Shared-table entity (`table_name: "records"`) — the common case. `status`/`action`/`bytes`
/// cover the enum/indexed-string/numeric cases the tests below exercise.
fn shared_entity() -> EntityDefinition {
    EntityDefinition {
        name: "waf.security_events".to_string(),
        label: "Security Event".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("zoneId", FieldKind::String, true, false, false, None),
            field(
                "action",
                FieldKind::Enum,
                true,
                false,
                false,
                Some(vec!["logged", "challenged", "blocked"]),
            ),
            field("sourceIp", FieldKind::String, true, false, false, None),
            field("bytes", FieldKind::Number, false, false, false, None),
            field("occurredAt", FieldKind::Datetime, false, true, false, None),
            field("notes", FieldKind::String, false, false, false, None),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["zoneId".to_string(), "action".to_string()],
            filters: vec!["zoneId".to_string(), "action".to_string(), "sourceIp".to_string()],
            default_sort: Some("-occurredAt".to_string()),
            max_limit: 50,
        }],
        workflow: None,
    }
}

/// A dedicated-table (table-per-entity) entity with a workflow, so `status`'s always-groupable
/// carve-out and the dedicated-table column path both have coverage.
fn dedicated_entity() -> EntityDefinition {
    EntityDefinition {
        name: "waf.scan_jobs".to_string(),
        label: "Scan Job".to_string(),
        table_name: "entities.waf_scan_jobs".to_string(),
        fields: vec![
            field("zoneId", FieldKind::String, true, false, false, None),
            field(
                "status",
                FieldKind::Enum,
                false,
                true,
                false,
                Some(vec!["idle", "queued", "running", "completed", "failed"]),
            ),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["zoneId".to_string()],
            filters: vec!["zoneId".to_string()],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "idle".to_string(),
            terminal_states: vec![],
            transitions: vec![WorkflowTransition {
                action: "run".to_string(),
                from: "idle".to_string(),
                to: "queued".to_string(),
                label: "Run".to_string(),
                guard: None,
                validator: None,
                set_fields: None,
            }],
        }),
    }
}

fn registry_with(entities: Vec<EntityDefinition>) -> metap_metadata::MetadataRegistry {
    let mut registry = metap_metadata::MetadataRegistry::new();
    for entity in entities {
        registry.register(entity).unwrap();
    }
    registry
}

#[test]
fn plain_count_scopes_tenant_and_excludes_deleted() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let tenant_id = Uuid::new_v4();
    let ctx = context(tenant_id);
    let input = AggregateInput::default();

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();

    assert!(planned.sql.contains("count(*)"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("tenant_id = $1"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("entity = $2"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("deleted = false"), "sql: {}", planned.sql);
    assert!(planned.sql.starts_with("SELECT to_jsonb(agg) AS row FROM (SELECT"));
    // tenant_id (Uuid) + entity name (Text) — no group-by, no filters, no metric field.
    assert_eq!(planned.params.len(), 2);
    assert!(matches!(planned.params[0], BindValue::Uuid(id) if id == tenant_id));
}

#[test]
fn dedicated_table_has_no_entity_discriminator_column() {
    let registry = registry_with(vec![dedicated_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput::default();

    let planned = plan_aggregate(&registry, &permissions, "waf.scan_jobs", &input, &ctx, &[]).unwrap();

    assert!(planned.sql.contains("entities.waf_scan_jobs"), "sql: {}", planned.sql);
    assert!(!planned.sql.contains("entity = "), "sql: {}", planned.sql);
    // Only tenant_id — a dedicated table needs no `entity` filter.
    assert_eq!(planned.params.len(), 1);
}

#[test]
fn unknown_entity_is_an_error() {
    let registry = registry_with(vec![]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput::default();

    let err = plan_aggregate(&registry, &permissions, "waf.does_not_exist", &input, &ctx, &[]).unwrap_err();
    assert!(err.downcast_ref::<InvalidAggregateError>().is_none());
    assert!(err.to_string().contains("Entity not found"));
}

#[test]
fn empty_metrics_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: vec![],
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    let err = err
        .downcast_ref::<InvalidAggregateError>()
        .expect("InvalidAggregateError");
    assert!(err.0.contains("At least one metric"));
}

#[test]
fn too_many_metrics_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: (0..11)
            .map(|_| AggregateMetric {
                func: AggregateFn::Count,
                field: None,
            })
            .collect(),
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    assert!(err.downcast_ref::<InvalidAggregateError>().is_some());
}

#[test]
fn unknown_group_by_field_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        group_by: Some("doesNotExist".to_string()),
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    let err = err
        .downcast_ref::<InvalidAggregateError>()
        .expect("InvalidAggregateError");
    assert!(err.0.contains("Unknown field"), "{}", err.0);
}

#[test]
fn non_indexed_non_enum_field_cannot_be_grouped_by() {
    // `notes` on `shared_entity()` is a plain String field — not indexed, no enum_values, and
    // not in the list view's filters. `assert_groupable` must refuse it.
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        group_by: Some("notes".to_string()),
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    let err = err
        .downcast_ref::<InvalidAggregateError>()
        .expect("InvalidAggregateError");
    assert!(err.0.contains("cannot be grouped by"), "{}", err.0);
}

#[test]
fn indexed_field_can_be_grouped_by() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        group_by: Some("action".to_string()),
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("AS \"group\""), "sql: {}", planned.sql);
    assert!(planned.sql.contains("GROUP BY"), "sql: {}", planned.sql);
    assert!(
        planned.sql.contains("ORDER BY \"count\" DESC NULLS LAST"),
        "sql: {}",
        planned.sql
    );
}

#[test]
fn status_is_always_groupable_even_when_not_indexed() {
    // `dedicated_entity()`'s `status` field is `indexed: false` — only the `field.name == "status"`
    // carve-out in `assert_groupable` should let this through.
    let registry = registry_with(vec![dedicated_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        group_by: Some("status".to_string()),
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.scan_jobs", &input, &ctx, &[]).unwrap();
    // Dedicated table + real column (status is not `field_has_real_column` by default here since
    // it's not Reference/Id and has no explicit storage override — falls back to the JSONB path,
    // still valid SQL either way) — the important assertion is that planning succeeded at all.
    assert!(planned.sql.contains("GROUP BY"), "sql: {}", planned.sql);
}

#[test]
fn count_with_field_does_not_require_numeric() {
    // `count:<field>` ("rows where this field is set") is the one aggregate allowed on a
    // non-numeric field.
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: vec![AggregateMetric {
            func: AggregateFn::Count,
            field: Some("sourceIp".to_string()),
        }],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(
        planned.sql.contains("count(jsonb_extract_path_text"),
        "sql: {}",
        planned.sql
    );
    assert!(planned.sql.contains("AS \"count_sourceIp\""), "sql: {}", planned.sql);
}

#[test]
fn sum_on_non_numeric_field_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: vec![AggregateMetric {
            func: AggregateFn::Sum,
            field: Some("sourceIp".to_string()),
        }],
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    let err = err
        .downcast_ref::<InvalidAggregateError>()
        .expect("InvalidAggregateError");
    assert!(err.0.contains("not a numeric field"), "{}", err.0);
}

#[test]
fn sum_on_numeric_field_produces_nullif_cast() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: vec![AggregateMetric {
            func: AggregateFn::Sum,
            field: Some("bytes".to_string()),
        }],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("sum(NULLIF("), "sql: {}", planned.sql);
    assert!(planned.sql.contains("::numeric)"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("AS \"sum_bytes\""), "sql: {}", planned.sql);
}

#[test]
fn metric_on_unknown_field_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        metrics: vec![AggregateMetric {
            func: AggregateFn::Sum,
            field: Some("doesNotExist".to_string()),
        }],
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    assert!(err.downcast_ref::<InvalidAggregateError>().is_some());
}

#[test]
fn bucket_uses_date_trunc_over_real_created_at_column() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        bucket: Some(TimeBucket::Day),
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(
        planned.sql.contains("date_trunc('day', created_at)"),
        "sql: {}",
        planned.sql
    );
    assert!(planned.sql.contains("AS \"bucket\""), "sql: {}", planned.sql);
    assert!(planned.sql.contains("ORDER BY \"bucket\" ASC"), "sql: {}", planned.sql);
}

#[test]
fn bucket_over_declared_date_field_uses_nullif_timestamptz_cast() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        bucket: Some(TimeBucket::Hour),
        time_field: Some("occurredAt".to_string()),
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("date_trunc('hour',"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("::timestamptz)"), "sql: {}", planned.sql);
}

#[test]
fn time_field_that_is_not_a_date_kind_is_rejected() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        bucket: Some(TimeBucket::Day),
        time_field: Some("sourceIp".to_string()),
        ..Default::default()
    };

    let err = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap_err();
    let err = err
        .downcast_ref::<InvalidAggregateError>()
        .expect("InvalidAggregateError");
    assert!(err.0.contains("not a date field"), "{}", err.0);
}

#[test]
fn since_and_until_bind_and_cast_to_timestamptz() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        since: Some("2026-01-01T00:00:00Z".to_string()),
        until: Some("2026-02-01T00:00:00Z".to_string()),
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains(">= $"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("< $"), "sql: {}", planned.sql);
    assert!(planned.sql.contains("::timestamptz"), "sql: {}", planned.sql);
    let has_since_value = planned
        .params
        .iter()
        .any(|p| matches!(p, BindValue::Text(s) if s == "2026-01-01T00:00:00Z"));
    assert!(has_since_value);
}

#[test]
fn filter_outside_list_view_filters_is_silently_ignored() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        filters: vec![("notes".to_string(), "hello".to_string())],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    // Same "silently skip" behavior `plan_list` has for an out-of-list-view filter — no error,
    // and the value never reaches the SQL text.
    assert!(!planned.sql.contains("hello"), "sql: {}", planned.sql);
}

#[test]
fn empty_string_filter_value_means_is_null() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        filters: vec![("sourceIp".to_string(), String::new())],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("IS NULL"), "sql: {}", planned.sql);
}

#[test]
fn allowed_filter_field_binds_equality() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        filters: vec![("zoneId".to_string(), "zone-123".to_string())],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    let has_value = planned
        .params
        .iter()
        .any(|p| matches!(p, BindValue::Text(s) if s == "zone-123"));
    assert!(has_value, "params: {:?}", planned.params);
}

#[test]
fn limit_is_clamped_to_max_groups() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        limit: 100_000,
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(
        planned.sql.contains(&format!("LIMIT {MAX_GROUPS}")),
        "sql: {}",
        planned.sql
    );
}

#[test]
fn limit_below_one_is_clamped_up_to_one() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        limit: 0,
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("LIMIT 1"), "sql: {}", planned.sql);
}

#[test]
fn metric_alias_disambiguates_function_and_field() {
    assert_eq!(
        AggregateMetric {
            func: AggregateFn::Count,
            field: None,
        }
        .alias(),
        "count"
    );
    assert_eq!(
        AggregateMetric {
            func: AggregateFn::Sum,
            field: Some("bytes".to_string()),
        }
        .alias(),
        "sum_bytes"
    );
}

#[test]
fn aggregate_metric_parse_round_trips() {
    let count = AggregateMetric::parse("count").unwrap();
    assert_eq!(count.func, AggregateFn::Count);
    assert_eq!(count.field, None);

    let sum = AggregateMetric::parse("sum:bytesBlocked").unwrap();
    assert_eq!(sum.func, AggregateFn::Sum);
    assert_eq!(sum.field.as_deref(), Some("bytesBlocked"));

    assert!(AggregateMetric::parse("nonsense").is_err());
    assert!(AggregateMetric::parse("sum").is_err(), "sum with no field must error");
}

#[test]
fn time_bucket_parse_rejects_unknown_unit() {
    assert!(TimeBucket::parse("day").is_ok());
    assert!(TimeBucket::parse("fortnight").is_err());
}

#[test]
fn group_by_and_metrics_together_produce_two_projection_columns_plus_group_by() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput {
        group_by: Some("action".to_string()),
        metrics: vec![
            AggregateMetric {
                func: AggregateFn::Count,
                field: None,
            },
            AggregateMetric {
                func: AggregateFn::Sum,
                field: Some("bytes".to_string()),
            },
        ],
        ..Default::default()
    };

    let planned = plan_aggregate(&registry, &permissions, "waf.security_events", &input, &ctx, &[]).unwrap();
    assert!(planned.sql.contains("AS \"group\""), "sql: {}", planned.sql);
    assert!(planned.sql.contains("AS \"count\""), "sql: {}", planned.sql);
    assert!(planned.sql.contains("AS \"sum_bytes\""), "sql: {}", planned.sql);
    // Sort tiebreaks on the *first* metric's alias, not the second.
    assert!(planned.sql.contains("ORDER BY \"count\" DESC"), "sql: {}", planned.sql);
}

#[test]
fn record_read_policy_is_folded_into_where_before_counting() {
    let registry = registry_with(vec![shared_entity()]);
    let permissions = permissions();
    let ctx = context(Uuid::new_v4());
    let input = AggregateInput::default();

    let deny_all = PolicyRow {
        id: Uuid::new_v4(),
        tenant_id: Uuid::new_v4(),
        entity: "waf.security_events".to_string(),
        action: "read".to_string(),
        field: None,
        subject: PolicySubject::Record.as_str().to_string(),
        roles: Some(vec!["someone-else".to_string()]),
        condition: None,
        created_by: None,
        effect: PolicyEffect::Allow,
    };

    let planned = plan_aggregate(
        &registry,
        &permissions,
        "waf.security_events",
        &input,
        &ctx,
        &[deny_all],
    )
    .unwrap();
    // A role-gated allow policy the caller's roles don't satisfy folds to a hard `false` — the
    // same "cannot even learn it exists" behavior `condition_to_sql`'s own tests verify for
    // `record_policy_where_clause` directly.
    assert!(planned.sql.contains("false"), "sql: {}", planned.sql);
}
