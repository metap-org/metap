//! E2E tests running real generated SQL against the repo's real dev Postgres (see
//! `CLAUDE.md`'s Commands section). `QueryPlanner` is the highest-precision-risk module in
//! this codebase (see `docs/architectures/09-adr.md`), so it's verified by comparing real
//! query results, not just unit-tested in isolation. `#[ignore]`d so a
//! plain `cargo test` (unit tests only) never touches a database — run these explicitly
//! with `cargo test -- --ignored` once the dev DB is up. Unit tests (pure logic, no DB) live
//! in `src/*.rs`; this file is the DB-dependent counterpart, kept structurally and
//! by-default separate from them.

use async_trait::async_trait;
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::{ExplainOptions, PermissionService, PolicyRow, PolicyStore, PolicySubject, RequestContext};
use metap_query::{apply_params, plan_list, ListInput};
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use uuid::Uuid;

/// `plan_list` only ever calls `PermissionService::scoped_tenant`, which never touches the
/// store — this stub exists purely so `PermissionService::new` has something to hold.
struct UnusedPolicyStore;

#[async_trait]
impl PolicyStore for UnusedPolicyStore {
    async fn find_context_policies(&self, _: Uuid, _: &str, _: &str) -> anyhow::Result<Vec<PolicyRow>> {
        unreachable!("plan_list never calls this")
    }
    async fn load_all_policies(&self, _: Uuid, _: &str) -> anyhow::Result<Vec<PolicyRow>> {
        unreachable!("plan_list never calls this")
    }
    async fn find_explain_policies(
        &self,
        _: Uuid,
        _: &str,
        _: &str,
        _: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        unreachable!("plan_list never calls this")
    }
    async fn list_policies(&self, _: Uuid, _: Option<&str>) -> anyhow::Result<Vec<PolicyRow>> {
        unreachable!("plan_list never calls this")
    }
    async fn create_policy(
        &self,
        _: Uuid,
        _: &str,
        _: &str,
        _: Option<Vec<String>>,
        _: Option<metap_permission::PolicyCondition>,
        _: Option<Uuid>,
        _: Option<&str>,
        _: Option<PolicySubject>,
        _: metap_permission::PolicyEffect,
    ) -> anyhow::Result<PolicyRow> {
        unreachable!("plan_list never calls this")
    }
    async fn delete_policy(&self, _: Uuid, _: Uuid) -> anyhow::Result<()> {
        unreachable!("plan_list never calls this")
    }
    async fn sync_basic_policies(
        &self,
        _: Uuid,
        _: &str,
        _: Vec<(Option<String>, String)>,
        _: Option<Uuid>,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        unreachable!("plan_list never calls this")
    }
}

fn field(name: &str, kind: FieldKind, sortable: bool, searchable: bool) -> EntityField {
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
        searchable: searchable.then_some(true),
        search_mode: searchable.then(|| "substring".to_string()),
        sortable: sortable.then_some(true),
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
    }
}

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.widgets".to_string(),
        label: "Widget".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("name", FieldKind::String, true, true),
            field("status", FieldKind::String, false, false),
            field("score", FieldKind::Number, true, false),
        ],
        list_views: vec![
            EntityListView {
                name: "default".to_string(),
                label: "Default".to_string(),
                fields: vec!["name".to_string(), "status".to_string()],
                filters: vec!["status".to_string(), "name".to_string()],
                default_sort: Some("-createdAt".to_string()),
                max_limit: 50,
            },
            // A second list view (`docs/features/01-fe-platform-overhaul.md`'s scoped gap —
            // `plan_list` used to only ever look at `list_views.first()`) with a deliberately
            // different `max_limit`/`default_sort`/`filters` set from "default", so a test can
            // tell whether `ListInput.list_view` actually changed which one was used.
            EntityListView {
                name: "by_score".to_string(),
                label: "By Score".to_string(),
                fields: vec!["name".to_string(), "score".to_string()],
                filters: vec!["score".to_string()],
                default_sort: Some("-score".to_string()),
                max_limit: 10,
            },
        ],
        workflow: None,
    }
}

async fn insert_fixture(pool: &PgPool, tenant_id: Uuid, name: &str, status: &str, score: i64, deleted: bool) -> Uuid {
    let id = Uuid::new_v4();
    sqlx::query(
        "INSERT INTO records (id, tenant_id, entity, data, deleted) \
         VALUES ($1, $2, 'test.widgets', $3, $4)",
    )
    .bind(id)
    .bind(tenant_id)
    .bind(serde_json::json!({ "name": name, "status": status, "score": score }))
    .bind(deleted)
    .execute(pool)
    .await
    .expect("insert fixture row");
    id
}

/// Same shape as `insert_fixture`, but the `score` key is entirely absent from `data` — a real
/// `NULL` under `jsonb_extract_path_text(data, 'score')`, not just a JSON `null`, matching how a
/// field genuinely never set (rather than explicitly nulled) looks on a real record.
async fn insert_fixture_no_score(pool: &PgPool, tenant_id: Uuid, name: &str) -> Uuid {
    let id = Uuid::new_v4();
    sqlx::query(
        "INSERT INTO records (id, tenant_id, entity, data, deleted) \
         VALUES ($1, $2, 'test.widgets', $3, false)",
    )
    .bind(id)
    .bind(tenant_id)
    .bind(serde_json::json!({ "name": name, "status": "active" }))
    .execute(pool)
    .await
    .expect("insert fixture row with no score");
    id
}

async fn run_plan(pool: &PgPool, planned: &metap_query::PlannedListQuery) -> Vec<Uuid> {
    let sql = format!(
        "SELECT id FROM records WHERE {} ORDER BY {} LIMIT {}",
        planned.where_sql, planned.order_by_sql, planned.limit
    );
    let query = sqlx::query(&sql);
    let query = apply_params(query, &planned.params);
    query
        .fetch_all(pool)
        .await
        .unwrap_or_else(|err| panic!("query failed: {err}\nsql: {sql}"))
        .into_iter()
        .map(|row| row.get::<Uuid, _>("id"))
        .collect()
}

struct Harness {
    pool: PgPool,
    registry: MetadataRegistry,
    permissions: PermissionService,
    tenant_id: Uuid,
}

async fn setup() -> Harness {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .unwrap();
    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(UnusedPolicyStore));
    Harness {
        pool,
        registry,
        permissions,
        tenant_id: Uuid::new_v4(),
    }
}

impl Harness {
    fn context(&self) -> RequestContext {
        RequestContext {
            tenant_id: self.tenant_id.to_string(),
            user_id: None,
            roles: None,
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        }
    }

    async fn cleanup(&self, ids: &[Uuid]) {
        for id in ids {
            sqlx::query("DELETE FROM records WHERE id = $1")
                .bind(id)
                .execute(&self.pool)
                .await
                .ok();
        }
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn tenant_scoping_and_soft_delete_exclusion() {
    let h = setup().await;

    let other_tenant = Uuid::new_v4();
    let visible = insert_fixture(&h.pool, h.tenant_id, "alpha", "active", 1, false).await;
    let deleted = insert_fixture(&h.pool, h.tenant_id, "beta", "active", 2, true).await;
    let other = insert_fixture(&h.pool, other_tenant, "gamma", "active", 3, false).await;

    let input = ListInput {
        limit: 50,
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    let ids = run_plan(&h.pool, &planned).await;

    assert!(ids.contains(&visible));
    assert!(!ids.contains(&deleted), "soft-deleted row must be excluded");
    assert!(!ids.contains(&other), "other tenant's row must be excluded");

    h.cleanup(&[visible, deleted, other]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn substring_filter_matches_ilike_case_insensitively() {
    let h = setup().await;

    let matching = insert_fixture(&h.pool, h.tenant_id, "Acme Corp", "active", 1, false).await;
    let non_matching = insert_fixture(&h.pool, h.tenant_id, "Other Co", "active", 2, false).await;

    let input = ListInput {
        limit: 50,
        filters: vec![("name".to_string(), "acme".to_string())],
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    let ids = run_plan(&h.pool, &planned).await;

    assert!(ids.contains(&matching));
    assert!(!ids.contains(&non_matching));

    h.cleanup(&[matching, non_matching]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn exact_filter_on_non_searchable_field() {
    let h = setup().await;

    let active = insert_fixture(&h.pool, h.tenant_id, "one", "active", 1, false).await;
    let draft = insert_fixture(&h.pool, h.tenant_id, "two", "draft", 2, false).await;

    let input = ListInput {
        limit: 50,
        filters: vec![("status".to_string(), "active".to_string())],
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    let ids = run_plan(&h.pool, &planned).await;

    assert!(ids.contains(&active));
    assert!(!ids.contains(&draft));

    h.cleanup(&[active, draft]).await;
}

/// Found live (2026-08-24, `../metap-demo-jira`'s backlog view — filtering `sprint` for "not yet
/// assigned"): an empty filter value used to bind straight into the equality branch, which is
/// harmless for a plain text field but a real 500 for a `uuid`-cast one
/// (`invalid input syntax for type uuid: ""`, `sort_field_expression`'s `cast_suffix()`). Fixed
/// by treating an empty value as `IS NULL` for every field, not just uuid-typed ones — this test
/// covers the generic-table JSONB path (`data->>'field'`, no cast involved), the uuid-cast path
/// is exercised live against `../metap-demo-jira`'s real `Reference` column.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn empty_filter_value_matches_unset_field() {
    let h = setup().await;

    let with_status = insert_fixture(&h.pool, h.tenant_id, "one", "active", 1, false).await;
    let without_status = Uuid::new_v4();
    sqlx::query(
        "INSERT INTO records (id, tenant_id, entity, data, deleted) VALUES ($1, $2, 'test.widgets', $3, false)",
    )
    .bind(without_status)
    .bind(h.tenant_id)
    .bind(serde_json::json!({ "name": "two", "score": 2 }))
    .execute(&h.pool)
    .await
    .expect("insert fixture row with no status key");

    let input = ListInput {
        limit: 50,
        filters: vec![("status".to_string(), String::new())],
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    let ids = run_plan(&h.pool, &planned).await;

    assert!(ids.contains(&without_status));
    assert!(!ids.contains(&with_status));

    h.cleanup(&[with_status, without_status]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn default_sort_is_used_when_no_sort_given_and_limit_is_clamped() {
    let h = setup().await;

    let a = insert_fixture(&h.pool, h.tenant_id, "a", "active", 1, false).await;
    tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    let b = insert_fixture(&h.pool, h.tenant_id, "b", "active", 2, false).await;

    // maxLimit on the list view is 50; requesting 500 must clamp down to it.
    let input = ListInput {
        limit: 500,
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    assert_eq!(planned.limit, 50);
    assert_eq!(planned.resolved_sort.field, "createdAt");
    assert!(planned.resolved_sort.descending);

    let ids = run_plan(&h.pool, &planned).await;
    let pos_a = ids.iter().position(|id| id == &a).unwrap();
    let pos_b = ids.iter().position(|id| id == &b).unwrap();
    assert!(
        pos_b < pos_a,
        "newer row (b) must sort before older row (a) on default -createdAt"
    );

    h.cleanup(&[a, b]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn sortable_field_ascending_order() {
    let h = setup().await;

    let low = insert_fixture(&h.pool, h.tenant_id, "low", "active", 1, false).await;
    let high = insert_fixture(&h.pool, h.tenant_id, "high", "active", 9, false).await;

    let input = ListInput {
        limit: 50,
        sort: Some("score".to_string()),
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    assert_eq!(
        planned.resolved_sort,
        metap_query::ResolvedSort {
            field: "score".to_string(),
            descending: false
        }
    );

    let ids = run_plan(&h.pool, &planned).await;
    let pos_low = ids.iter().position(|id| id == &low).unwrap();
    let pos_high = ids.iter().position(|id| id == &high).unwrap();
    assert!(pos_low < pos_high);

    h.cleanup(&[low, high]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn unsortable_requested_field_falls_back_to_default_sort() {
    let h = setup().await;

    let input = ListInput {
        limit: 50,
        sort: Some("status".to_string()),
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    // "status" is not declared sortable — must fall back to the list view's defaultSort.
    assert_eq!(planned.resolved_sort.field, "createdAt");
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn keyset_pagination_produces_disjoint_consecutive_pages() {
    let h = setup().await;

    let mut ids = Vec::new();
    for i in 0..5 {
        ids.push(insert_fixture(&h.pool, h.tenant_id, &format!("row{i}"), "active", i, false).await);
        tokio::time::sleep(std::time::Duration::from_millis(5)).await;
    }

    let page1_input = ListInput {
        limit: 2,
        ..Default::default()
    };
    let page1_planned = plan_list(
        &h.registry,
        &h.permissions,
        "test.widgets",
        &page1_input,
        &h.context(),
        &[],
    )
    .unwrap();
    let page1 = run_plan(&h.pool, &page1_planned).await;
    assert_eq!(page1.len(), 2);

    // Build a cursor from the last row of page 1, matching CrudService's real job
    // (Migration Order step 7) — reconstructed here directly since that's not built yet.
    let last_id = page1[1];
    let created_at: chrono::DateTime<chrono::Utc> = sqlx::query("SELECT created_at FROM records WHERE id = $1")
        .bind(last_id)
        .fetch_one(&h.pool)
        .await
        .unwrap()
        .get("created_at");
    let cursor = metap_query::Cursor {
        field: "createdAt".to_string(),
        value: Some(created_at.to_rfc3339()),
        id: last_id.to_string(),
        dir: metap_query::SortDir::Desc,
    };
    let encoded = metap_query::encode_cursor(&cursor);

    let page2_input = ListInput {
        limit: 2,
        cursor: Some(encoded),
        ..Default::default()
    };
    let page2_planned = plan_list(
        &h.registry,
        &h.permissions,
        "test.widgets",
        &page2_input,
        &h.context(),
        &[],
    )
    .unwrap();
    let page2 = run_plan(&h.pool, &page2_planned).await;

    assert!(
        page1.iter().all(|id| !page2.contains(id)),
        "page 2 must not repeat any row from page 1 (page1={page1:?}, page2={page2:?})"
    );
    assert!(!page2.is_empty());

    h.cleanup(&ids).await;
}

/// Regression for the finding in `AUDIT_2.md`: a `NULL` sort value used to be encoded as an
/// empty string in the cursor, which can never satisfy `sort_expr > ''`/`sort_expr = ''` under
/// SQL's three-valued logic — every row from the first `NULL` onward silently vanished from
/// later pages. Sorts `score` ascending (`NULLS LAST`, Postgres's own default, now made explicit
/// in `plan_list`) across a mix of scored and unscored rows, paging with `limit: 2` through to
/// the end, and asserts every single row was eventually seen — including the `NULL`-scored ones.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn keyset_pagination_does_not_lose_rows_with_a_null_sort_value() {
    let h = setup().await;

    let mut ids = Vec::new();
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-1", "active", 1, false).await);
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-2", "active", 2, false).await);
    ids.push(insert_fixture_no_score(&h.pool, h.tenant_id, "unscored-1").await);
    ids.push(insert_fixture_no_score(&h.pool, h.tenant_id, "unscored-2").await);
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-3", "active", 3, false).await);

    let mut seen: Vec<Uuid> = Vec::new();
    let mut cursor: Option<String> = None;
    for _ in 0..10 {
        let input = ListInput {
            limit: 2,
            sort: Some("score".to_string()),
            cursor: cursor.clone(),
            ..Default::default()
        };
        let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
        let sql = format!(
            "SELECT id, jsonb_extract_path_text(data, 'score') AS score FROM records WHERE {} ORDER BY {} LIMIT {}",
            planned.where_sql, planned.order_by_sql, planned.limit
        );
        let query = apply_params(sqlx::query(&sql), &planned.params);
        let rows = query
            .fetch_all(&h.pool)
            .await
            .unwrap_or_else(|err| panic!("query failed: {err}\nsql: {sql}"));
        if rows.is_empty() {
            break;
        }
        for row in &rows {
            seen.push(row.get::<Uuid, _>("id"));
        }
        let last = rows.last().unwrap();
        cursor = Some(metap_query::encode_cursor(&metap_query::Cursor {
            field: "score".to_string(),
            value: last.get::<Option<String>, _>("score"),
            id: last.get::<Uuid, _>("id").to_string(),
            dir: metap_query::SortDir::Asc,
        }));
    }

    seen.sort();
    let mut expected = ids.clone();
    expected.sort();
    assert_eq!(
        seen, expected,
        "every row must be seen exactly once across all pages, including the NULL-scored ones"
    );

    h.cleanup(&ids).await;
}

/// Same as `keyset_pagination_does_not_lose_rows_with_a_null_sort_value` but descending —
/// `cursor_condition`'s `DESC` branches are genuinely different code from its `ASC` ones (the
/// leading vs. trailing `NULL` block under `NULLS FIRST` vs. `NULLS LAST`), so this is a
/// distinct code path worth its own live check, not just the mirror of the `ASC` case.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn keyset_pagination_does_not_lose_rows_with_a_null_sort_value_descending() {
    let h = setup().await;

    let mut ids = Vec::new();
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-1", "active", 1, false).await);
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-2", "active", 2, false).await);
    ids.push(insert_fixture_no_score(&h.pool, h.tenant_id, "unscored-1").await);
    ids.push(insert_fixture_no_score(&h.pool, h.tenant_id, "unscored-2").await);
    ids.push(insert_fixture(&h.pool, h.tenant_id, "scored-3", "active", 3, false).await);

    let mut seen: Vec<Uuid> = Vec::new();
    let mut cursor: Option<String> = None;
    for _ in 0..10 {
        let input = ListInput {
            limit: 2,
            sort: Some("-score".to_string()),
            cursor: cursor.clone(),
            ..Default::default()
        };
        let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
        let sql = format!(
            "SELECT id, jsonb_extract_path_text(data, 'score') AS score FROM records WHERE {} ORDER BY {} LIMIT {}",
            planned.where_sql, planned.order_by_sql, planned.limit
        );
        let query = apply_params(sqlx::query(&sql), &planned.params);
        let rows = query
            .fetch_all(&h.pool)
            .await
            .unwrap_or_else(|err| panic!("query failed: {err}\nsql: {sql}"));
        if rows.is_empty() {
            break;
        }
        for row in &rows {
            seen.push(row.get::<Uuid, _>("id"));
        }
        let last = rows.last().unwrap();
        cursor = Some(metap_query::encode_cursor(&metap_query::Cursor {
            field: "score".to_string(),
            value: last.get::<Option<String>, _>("score"),
            id: last.get::<Uuid, _>("id").to_string(),
            dir: metap_query::SortDir::Desc,
        }));
    }

    seen.sort();
    let mut expected = ids.clone();
    expected.sort();
    assert_eq!(
        seen, expected,
        "every row must be seen exactly once across all pages, including the NULL-scored ones"
    );

    h.cleanup(&ids).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn cursor_mismatched_with_current_sort_is_rejected() {
    let h = setup().await;

    let cursor = metap_query::Cursor {
        field: "score".to_string(), // query below sorts by createdAt (default) — mismatch
        value: Some("1".to_string()),
        id: Uuid::new_v4().to_string(),
        dir: metap_query::SortDir::Desc,
    };
    let encoded = metap_query::encode_cursor(&cursor);

    let input = ListInput {
        limit: 50,
        cursor: Some(encoded),
        ..Default::default()
    };
    let result = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]);
    assert!(result.is_err());
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_view_query_param_selects_a_non_default_list_view() {
    let h = setup().await;
    let a = insert_fixture(&h.pool, h.tenant_id, "alpha", "active", 1, false).await;
    let b = insert_fixture(&h.pool, h.tenant_id, "beta", "active", 2, false).await;

    // "by_score" (unlike "default") sorts by score descending, has its own max_limit, and
    // doesn't allow filtering by "status" at all.
    let input = ListInput {
        limit: 50,
        list_view: Some("by_score".to_string()),
        filters: vec![("status".to_string(), "inactive".to_string())],
        ..Default::default()
    };
    let planned = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]).unwrap();
    assert_eq!(planned.limit, 10, "by_score's own max_limit, not default's 50");
    let ids = run_plan(&h.pool, &planned).await;
    assert_eq!(
        ids,
        vec![b, a],
        "by_score's own default sort (-score); the status filter must be ignored (not in by_score's filters)"
    );

    h.cleanup(&[a, b]).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_view_query_param_unknown_name_is_rejected() {
    let h = setup().await;

    let input = ListInput {
        list_view: Some("does_not_exist".to_string()),
        ..Default::default()
    };
    let result = plan_list(&h.registry, &h.permissions, "test.widgets", &input, &h.context(), &[]);
    let err = result.err().expect("unknown list view must be rejected");
    assert!(err.downcast_ref::<metap_query::UnknownListViewError>().is_some());
}
