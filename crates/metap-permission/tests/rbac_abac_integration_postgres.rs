//! Security/regression suite (`testing/security/checklist.md`) — `src/policy_condition.rs`'s
//! `#[cfg(test)]` module already covers RBAC/ABAC logic in isolation (role gates, operators,
//! deny-overrides-allow, ~20 unit tests); this crate had **no** `tests/` directory at all before
//! this file — every one of those was a pure-function test, never a real `PermissionService`
//! round-tripping policies through Postgres, and every existing e2e elsewhere in the repo only
//! ever exercises the `"admin"` role (`context.is_admin()` bypasses both the context gate and
//! every record-level condition, so an admin-only test can't catch a broken non-admin path).
//! `#[ignore]`d — see `crates/metap-query/tests/query_planner_postgres.rs`'s doc comment for the
//! convention.
//!
//! No dependency on `metap-control::PostgresPolicyStore` here — that impl lives in
//! `metap-control` specifically to avoid a dependency cycle
//! (`metap-control -> metap-permission`; see `src/policy_store.rs`'s `row_from_sql` doc comment).
//! This file's `TestPolicyStore` reuses that same `row_from_sql` row-mapping function and talks
//! to the `policies` table directly (no `Router`/schema-per-tenant routing — this project's
//! tenants only ever run in `public` schema today, see `CLAUDE.md`'s `metap-control` bullet),
//! which is enough to exercise `PermissionService`/`PermissionSnapshot` for real.

use async_trait::async_trait;
use metap_permission::{
    row_from_sql, EntityAction, ExplainOptions, JsonObject, PermissionService, PolicyCondition, PolicyEffect,
    PolicyRow, PolicyStore, PolicySubject, RequestContext,
};
use serde_json::json;
use sqlx::PgPool;
use uuid::Uuid;

struct TestPolicyStore(PgPool);

#[async_trait]
impl PolicyStore for TestPolicyStore {
    async fn find_context_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        let rows = sqlx::query(
            "SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND subject = 'context'",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .fetch_all(&self.0)
        .await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn load_all_policies(&self, tenant_id: Uuid, entity: &str) -> anyhow::Result<Vec<PolicyRow>> {
        let rows = sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2")
            .bind(tenant_id)
            .bind(entity)
            .fetch_all(&self.0)
            .await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn find_explain_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        _options: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        self.load_all_policies(tenant_id, entity)
            .await
            .map(|rows| rows.into_iter().filter(|r| r.action == action).collect())
    }

    async fn list_policies(&self, tenant_id: Uuid, entity: Option<&str>) -> anyhow::Result<Vec<PolicyRow>> {
        match entity {
            Some(e) => self.load_all_policies(tenant_id, e).await,
            None => {
                let rows = sqlx::query("SELECT * FROM policies WHERE tenant_id = $1")
                    .bind(tenant_id)
                    .fetch_all(&self.0)
                    .await?;
                rows.iter().map(row_from_sql).collect()
            }
        }
    }

    #[allow(clippy::too_many_arguments)]
    async fn create_policy(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        roles: Option<Vec<String>>,
        condition: Option<PolicyCondition>,
        created_by: Option<Uuid>,
        field: Option<&str>,
        subject: Option<PolicySubject>,
        effect: PolicyEffect,
    ) -> anyhow::Result<PolicyRow> {
        let row = sqlx::query(
            "INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect) \
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .bind(roles.map(sqlx::types::Json))
        .bind(condition.map(sqlx::types::Json))
        .bind(created_by)
        .bind(field)
        .bind(subject.unwrap_or(PolicySubject::Context).as_str())
        .bind(effect.as_str())
        .fetch_one(&self.0)
        .await?;
        row_from_sql(&row)
    }

    async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM policies WHERE tenant_id = $1 AND id = $2")
            .bind(tenant_id)
            .bind(id)
            .execute(&self.0)
            .await?;
        Ok(())
    }
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    sqlx::postgres::PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

fn service(pool: PgPool) -> PermissionService {
    PermissionService::new(Box::new(TestPolicyStore(pool)))
}

fn context(tenant_id: Uuid, role: &str) -> RequestContext {
    RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec![role.to_string()]),
        function_id: None,
        context_attributes: None,
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

/// The deny-by-default guarantee at the integration level, not just the unit-tested pure
/// function — a non-admin role with zero matching policies in a *real* `policies` table must be
/// denied, exactly as it would be for a brand-new entity nobody has written a policy for yet.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn non_admin_role_with_no_matching_policy_is_denied_by_default() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let permissions = service(pool.clone());

    let ctx = context(tenant_id, "editor");
    let decision = permissions.can_read_entity(&ctx, "test.rbac_widgets").await.unwrap();
    assert!(
        !decision.allowed,
        "no policy exists at all — must deny, not silently allow"
    );

    cleanup(&pool, tenant_id).await;
}

/// A context-subject role-gate policy must grant the role it names and refuse every other role
/// — the RBAC half, at the integration level, for a role that isn't `"admin"`.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn role_gate_policy_grants_the_named_role_and_denies_others() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let permissions = service(pool.clone());

    permissions
        .create_policy(
            tenant_id,
            "test.rbac_widgets",
            "read",
            Some(vec!["editor".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();

    let editor_decision = permissions
        .can_read_entity(&context(tenant_id, "editor"), "test.rbac_widgets")
        .await
        .unwrap();
    assert!(
        editor_decision.allowed,
        "editor role matches the policy — must be allowed"
    );

    let viewer_decision = permissions
        .can_read_entity(&context(tenant_id, "viewer"), "test.rbac_widgets")
        .await
        .unwrap();
    assert!(!viewer_decision.allowed, "viewer role does not match — must be denied");

    cleanup(&pool, tenant_id).await;
}

/// ABAC at the integration level: a record-subject condition comparing `fromContext` against the
/// record's own field, for a non-admin caller (`is_admin()` would otherwise bypass this
/// entirely — the exact reason every existing e2e using only `"admin"` context couldn't catch a
/// regression here).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn record_condition_allows_matching_department_and_denies_mismatched() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let permissions = service(pool.clone());

    permissions
        .create_policy(
            tenant_id,
            "test.rbac_tickets",
            "read",
            Some(vec!["employee".to_string()]),
            Some(PolicyCondition::Attribute {
                attribute: "departmentId".to_string(),
                op: metap_permission::ConditionOp::Eq,
                value: metap_permission::PolicyValue::FromContext {
                    from_context: "departmentId".to_string(),
                },
            }),
            None,
            None,
            Some(PolicySubject::Record),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();

    let snapshot = permissions.load_snapshot(tenant_id, "test.rbac_tickets").await.unwrap();

    let mut ctx = context(tenant_id, "employee");
    let mut attrs = serde_json::Map::new();
    attrs.insert("departmentId".to_string(), json!("sales"));
    ctx.context_attributes = Some(attrs);

    let mut same_dept: JsonObject = JsonObject::new();
    same_dept.insert("departmentId".to_string(), json!("sales"));
    let allowed = snapshot.can_perform_record_condition(&ctx, &same_dept, EntityAction::Read);
    assert!(
        allowed.allowed,
        "caller's departmentId matches the record's — must be allowed"
    );

    let mut other_dept: JsonObject = JsonObject::new();
    other_dept.insert("departmentId".to_string(), json!("engineering"));
    let denied = snapshot.can_perform_record_condition(&ctx, &other_dept, EntityAction::Read);
    assert!(
        !denied.allowed,
        "caller's departmentId does not match the record's — must be denied"
    );

    cleanup(&pool, tenant_id).await;
}

/// Deny-overrides-allow, round-tripped through a real `policies` table (the unit test in
/// `policy_condition.rs` proves the pure evaluation function; this proves the DB round trip
/// doesn't lose or reinterpret the `effect` column along the way).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn explicit_deny_policy_overrides_a_matching_allow_policy() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let permissions = service(pool.clone());

    permissions
        .create_policy(
            tenant_id,
            "test.rbac_docs",
            "read",
            Some(vec!["editor".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();
    permissions
        .create_policy(
            tenant_id,
            "test.rbac_docs",
            "read",
            Some(vec!["editor".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Deny,
        )
        .await
        .unwrap();

    let decision = permissions
        .can_read_entity(&context(tenant_id, "editor"), "test.rbac_docs")
        .await
        .unwrap();
    assert!(
        !decision.allowed,
        "an explicit deny must win even though an allow policy also matches"
    );

    cleanup(&pool, tenant_id).await;
}
