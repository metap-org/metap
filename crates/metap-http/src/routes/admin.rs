//! Admin-gated role/policy management over HTTP — the HTTP surface for
//! `metap_peripherals::assign_role`/`revoke_role`/`list_users` and
//! `PermissionService::list_policies`/`create_policy`/`delete_policy`/`explain`, all of
//! which previously existed only as functions with e2e coverage (see
//! `docs/architectures/11-risks.md`). Every handler here uses `AdminContext`, not
//! `AuthContext` — the `admin` role check happens once, in the extractor, so it can't be
//! forgotten on a future route added to this file.

use std::collections::HashMap;

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use metap_permission::{EntityAction, PolicyCondition, PolicyEffect, PolicyRow, PolicySubject};
use axum::{Json, Router};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;
use uuid::Uuid;

use crate::auth::AdminContext;
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

#[derive(Deserialize, ToSchema)]
struct CreateUserBody {
    email: String,
    password: String,
    #[serde(default)]
    roles: Vec<String>,
}

// Never actually constructed — doc-only, see `routes/health.rs`'s comment.
#[derive(Serialize, ToSchema)]
struct CreatedUserDto {
    #[serde(rename = "userId")]
    user_id: Uuid,
    email: String,
    roles: Vec<String>,
}

#[derive(Serialize, ToSchema)]
struct CreateUserResponse {
    data: CreatedUserDto,
}

/// Provisions a new local-login user (`docs/roadmap.md` Phase 15) — the admin-driven
/// counterpart to `dev-tools create-user`'s dev-seeding path; both call
/// `metap_peripherals::create_user`, so the two can't diverge on how a password gets hashed.
///
/// Runs the insert and every role assignment inside one `Router::begin(tenant_id)` transaction
/// (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20) rather than one connection per call —
/// besides reaching the right physical database for a `DedicatedDb`-strategy tenant, this also
/// closes a pre-existing atomicity gap: a role assignment failing partway used to leave a user
/// row committed with only some of `body.roles` granted, with no way to tell which; now the
/// whole request commits or rolls back together.
#[utoipa::path(
    post,
    path = "/admin/users",
    request_body = CreateUserBody,
    responses(
        (status = 201, description = "Created", body = CreateUserResponse),
        (status = 409, description = "Email already taken"),
    ),
)]
async fn create_user(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<CreateUserBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };

    let user = match metap_peripherals::create_user(&mut *tx, tenant_id, &body.email, &body.password).await {
        Ok(user) => user,
        Err(e) => {
            let is_duplicate_email = e
                .downcast_ref::<sqlx::Error>()
                .and_then(|e| e.as_database_error())
                .is_some_and(|e| e.is_unique_violation());
            if is_duplicate_email {
                return service_error_response(
                    409,
                    "email_taken",
                    Some("A user with this email already exists."),
                    None,
                );
            }
            return internal_error_response(e);
        }
    };

    let assigned_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    for role in &body.roles {
        if let Err(e) = metap_peripherals::assign_role(&mut *tx, tenant_id, user.id, role, assigned_by).await {
            return internal_error_response(e);
        }
    }

    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    (
        StatusCode::CREATED,
        Json(json!({ "data": { "userId": user.id, "email": user.email, "roles": body.roles } })),
    )
        .into_response()
}

// Never actually constructed — doc-only, see `routes/health.rs`'s comment. `policy_to_json`
// below builds this exact shape by hand (not `PolicyRow`'s own field names/casing), so this DTO
// mirrors `policy_to_json`'s output rather than the domain struct.
#[derive(Serialize, ToSchema)]
struct PolicyDto {
    id: Uuid,
    #[serde(rename = "tenantId")]
    tenant_id: Uuid,
    entity: String,
    action: String,
    field: Option<String>,
    subject: String,
    roles: Option<Vec<String>>,
    #[schema(value_type = Option<serde_json::Value>)]
    condition: Option<PolicyCondition>,
    #[serde(rename = "createdBy")]
    created_by: Option<Uuid>,
    effect: String,
}

fn policy_to_json(row: &PolicyRow) -> Value {
    json!({
        "id": row.id,
        "tenantId": row.tenant_id,
        "entity": row.entity,
        "action": row.action,
        "field": row.field,
        "subject": row.subject,
        "roles": row.roles,
        "condition": row.condition,
        "createdBy": row.created_by,
        "effect": row.effect.as_str(),
    })
}

// Never actually constructed — doc-only, see `routes/health.rs`'s comment.
#[derive(Serialize, ToSchema)]
struct UserRolesDto {
    #[serde(rename = "userId")]
    user_id: Uuid,
    roles: Vec<String>,
}

#[derive(Serialize, ToSchema)]
struct ListAdminUsersResponse {
    data: Vec<UserRolesDto>,
}

// Explicit operation_id: see `routes/users.rs`'s own `list_users` handler for why the default
// (bare function name) collides once both are in the same combined OpenApi document.
#[utoipa::path(
    get,
    path = "/admin/users",
    operation_id = "listAdminUsers",
    responses((status = 200, description = "OK", body = ListAdminUsersResponse)),
)]
async fn list_users(State(state): State<AppState>, AdminContext(context): AdminContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let result = metap_peripherals::list_users(&mut *tx, tenant_id).await;
    if result.is_ok() {
        if let Err(e) = tx.commit().await {
            return internal_error_response(e.into());
        }
    }
    match result {
        Ok(users) => {
            let data: Vec<Value> = users
                .into_iter()
                .map(|u| json!({ "userId": u.user_id, "roles": u.roles }))
                .collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, ToSchema)]
struct AssignRoleBody {
    role: String,
}

// Never actually constructed — doc-only, see `routes/health.rs`'s comment.
#[derive(Serialize, ToSchema)]
struct AssignedRoleDto {
    #[serde(rename = "userId")]
    user_id: Uuid,
    role: String,
}

#[derive(Serialize, ToSchema)]
struct AssignRoleResponse {
    data: AssignedRoleDto,
}

#[utoipa::path(
    post,
    path = "/admin/users/{userId}/roles",
    params(("userId" = Uuid, Path)),
    request_body = AssignRoleBody,
    responses((status = 201, description = "Created", body = AssignRoleResponse)),
)]
async fn assign_role(
    State(state): State<AppState>,
    Path(user_id): Path<Uuid>,
    AdminContext(context): AdminContext,
    Json(body): Json<AssignRoleBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let assigned_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let result = metap_peripherals::assign_role(&mut *tx, tenant_id, user_id, &body.role, assigned_by).await;
    match result {
        Ok(()) => {
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            (
                StatusCode::CREATED,
                Json(json!({ "data": { "userId": user_id, "role": body.role } })),
            )
                .into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[utoipa::path(
    delete,
    path = "/admin/users/{userId}/roles/{role}",
    params(("userId" = Uuid, Path), ("role" = String, Path)),
    responses((status = 204, description = "No content")),
)]
async fn revoke_role(
    State(state): State<AppState>,
    Path((user_id, role)): Path<(Uuid, String)>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    match metap_peripherals::revoke_role(&mut *tx, tenant_id, user_id, &role).await {
        Ok(()) => {
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

/// Explicit invalidation for `AUTH_CONTEXT_ENTITY`'s cache
/// (`docs/features/03-organization-identity.md`) — an operator's second option alongside just
/// waiting out the TTL (`metap_http::cache::ContextAttributesCache`) after editing a user's
/// membership record (e.g. `departmentId`), so an org-scoped policy takes effect on that user's
/// very next request instead of up to `AUTH_CONTEXT_CACHE_TTL_SECONDS` later. No-op (still
/// `204`) if the cache had nothing for this user — invalidating something that was never cached,
/// or already expired, isn't an error.
#[utoipa::path(
    post,
    path = "/admin/users/{userId}/context/invalidate",
    params(("userId" = Uuid, Path)),
    responses((status = 204, description = "No content")),
)]
async fn invalidate_context(
    State(state): State<AppState>,
    Path(user_id): Path<Uuid>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    state.context_attributes_cache.invalidate(tenant_id, user_id).await;
    StatusCode::NO_CONTENT.into_response()
}

#[derive(Serialize, ToSchema)]
struct ListPoliciesResponse {
    data: Vec<PolicyDto>,
}

#[utoipa::path(
    get,
    path = "/admin/policies",
    params(("entity" = Option<String>, Query)),
    responses((status = 200, description = "OK", body = ListPoliciesResponse)),
)]
async fn list_policies(
    State(state): State<AppState>,
    Query(params): Query<HashMap<String, String>>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let entity = params.get("entity").map(String::as_str);
    match state.permissions.list_policies(tenant_id, entity).await {
        Ok(rows) => {
            let data: Vec<Value> = rows.iter().map(policy_to_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, ToSchema)]
struct CreatePolicyBody {
    entity: String,
    action: String,
    roles: Option<Vec<String>>,
    #[schema(value_type = Option<serde_json::Value>)]
    condition: Option<PolicyCondition>,
    field: Option<String>,
    subject: Option<String>,
    /// `"allow"` (default) or `"deny"` — see `PolicyEffect`'s doc comment (`metap-permission`)
    /// for what `"deny"` actually does (overrides any matching `allow`, regardless of order).
    effect: Option<String>,
}

#[derive(Serialize, ToSchema)]
struct CreatePolicyResponse {
    data: PolicyDto,
}

#[utoipa::path(
    post,
    path = "/admin/policies",
    request_body = CreatePolicyBody,
    responses((status = 201, description = "Created", body = CreatePolicyResponse)),
)]
async fn create_policy(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<CreatePolicyBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let subject = match body.subject.as_deref() {
        Some("record") => PolicySubject::Record,
        _ => PolicySubject::Context,
    };
    let effect = body
        .effect
        .as_deref()
        .map(PolicyEffect::parse)
        .unwrap_or(PolicyEffect::Allow);
    match state
        .permissions
        .create_policy(
            tenant_id,
            &body.entity,
            &body.action,
            body.roles,
            body.condition,
            created_by,
            body.field.as_deref(),
            Some(subject),
            effect,
        )
        .await
    {
        Ok(row) => (StatusCode::CREATED, Json(json!({ "data": policy_to_json(&row) }))).into_response(),
        Err(e) => internal_error_response(e),
    }
}

/// Derived from `EntityAction::ALL` (single source of truth, `metap-permission`) rather than a
/// second hand-typed list — also the same list `GET /metadata/actions` (`routes/metadata.rs`)
/// exposes to the frontend, so the two can't drift.
const KNOWN_ACTIONS: [&str; 5] = [
    EntityAction::Read.as_str(),
    EntityAction::Create.as_str(),
    EntityAction::Update.as_str(),
    EntityAction::Delete.as_str(),
    EntityAction::Transition.as_str(),
];

#[derive(Deserialize, ToSchema)]
struct SeedDefaultPoliciesBody {
    entity: String,
    roles: Vec<String>,
    /// Defaults to all 5 known actions when omitted/empty — the common "grant this role
    /// everything on this entity" case right after onboarding it.
    #[serde(default)]
    actions: Vec<String>,
}

#[derive(Serialize, ToSchema)]
struct SeedDefaultPoliciesResponse {
    data: Vec<PolicyDto>,
}

/// Bulk-creates one context-subject, no-condition (pure RBAC) policy per action for `roles` on
/// `entity` — the ergonomic counterpart to `create_policy` now that `PermissionService` denies
/// by default when an entity/action has no policy at all (`docs/roadmap.md`'s permission-review
/// findings, 2026-08-21): a fresh entity or a fresh tenant used to just work for every role
/// until an operator restricted it; now an operator has to grant *something* before any
/// non-admin role can touch a new entity at all. One call here instead of up to 5 separate
/// `POST /admin/policies` calls. Idempotent per action in spirit but not in fact — calling this
/// twice with the same `entity`/`roles` creates duplicate policy rows (each still evaluates the
/// same OR-combined result, so it's harmless, just untidy); `DELETE /admin/policies/:id` is how
/// an operator cleans that up.
#[utoipa::path(
    post,
    path = "/admin/policies/seed-defaults",
    request_body = SeedDefaultPoliciesBody,
    responses((status = 201, description = "Created", body = SeedDefaultPoliciesResponse)),
)]
async fn seed_default_policies(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<SeedDefaultPoliciesBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    if body.roles.is_empty() {
        return service_error_response(400, "validation_failed", Some("`roles` must not be empty."), None);
    }
    let actions: Vec<&str> = if body.actions.is_empty() {
        KNOWN_ACTIONS.to_vec()
    } else {
        body.actions.iter().map(String::as_str).collect()
    };
    if let Some(unknown) = actions.iter().find(|a| !KNOWN_ACTIONS.contains(a)) {
        return service_error_response(
            400,
            "validation_failed",
            Some(&format!(
                "Unknown action \"{unknown}\" — must be one of {KNOWN_ACTIONS:?}."
            )),
            None,
        );
    }

    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let mut created = Vec::with_capacity(actions.len());
    for action in actions {
        match state
            .permissions
            .create_policy(
                tenant_id,
                &body.entity,
                action,
                Some(body.roles.clone()),
                None,
                created_by,
                None,
                Some(PolicySubject::Context),
                PolicyEffect::Allow,
            )
            .await
        {
            Ok(row) => created.push(policy_to_json(&row)),
            Err(e) => return internal_error_response(e),
        }
    }
    (StatusCode::CREATED, Json(json!({ "data": created }))).into_response()
}

#[utoipa::path(
    delete,
    path = "/admin/policies/{id}",
    params(("id" = Uuid, Path)),
    responses((status = 204, description = "No content")),
)]
async fn delete_policy(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match state.permissions.delete_policy(tenant_id, id).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, ToSchema)]
struct MatrixGrant {
    /// `None` = the matrix's pinned "Everyone" row (an open, `roles IS NULL` policy).
    role: Option<String>,
    action: String,
}

#[derive(Deserialize, ToSchema)]
struct SyncMatrixBody {
    entity: String,
    /// The complete desired set of `(role, action)` grants for this entity — anything not
    /// listed here is removed. See `PermissionService::sync_basic_policies`'s doc comment.
    grants: Vec<MatrixGrant>,
}

#[derive(Serialize, ToSchema)]
struct SyncMatrixResponse {
    data: Vec<PolicyDto>,
}

/// The RBAC permission matrix's single save call — replaces every basic-shaped policy for
/// `body.entity` with exactly `body.grants` in one atomic transaction
/// (`PolicyStore::sync_basic_policies`), instead of the matrix firing one `POST`/`DELETE` per
/// checkbox click. Never touches an Advanced-tab policy (condition/field/record-subject/deny) —
/// see that trait method's doc comment for the exact boundary.
#[utoipa::path(
    put,
    path = "/admin/policies/matrix",
    request_body = SyncMatrixBody,
    responses((status = 200, description = "OK", body = SyncMatrixResponse)),
)]
async fn sync_matrix_policies(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<SyncMatrixBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    if let Some(unknown) = body.grants.iter().find(|g| !KNOWN_ACTIONS.contains(&g.action.as_str())) {
        return service_error_response(
            400,
            "validation_failed",
            Some(&format!(
                "Unknown action \"{}\" — must be one of {KNOWN_ACTIONS:?}.",
                unknown.action
            )),
            None,
        );
    }

    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let grants: Vec<(Option<String>, String)> = body.grants.into_iter().map(|g| (g.role, g.action)).collect();
    match state
        .permissions
        .sync_basic_policies(tenant_id, &body.entity, grants, created_by)
        .await
    {
        Ok(rows) => {
            let data: Vec<Value> = rows.iter().map(policy_to_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, ToSchema)]
struct ExplainBody {
    entity: String,
    action: String,
    field: Option<String>,
    #[schema(value_type = Option<serde_json::Value>)]
    record: Option<serde_json::Map<String, Value>>,
}

/// The response body is `{"data": PolicyExplanation}` (`metap-permission`) — left undocumented
/// beyond the request shape, same fidelity the old hand-written `openapi_paths::admin_paths` had
/// for this route (no response schema either), since `PolicyExplanation`/`PolicyTraceEntry`
/// don't derive `ToSchema` and adding it is out of scope for this pass.
#[utoipa::path(
    post,
    path = "/admin/policies/explain",
    request_body = ExplainBody,
    responses((status = 200, description = "OK")),
)]
async fn explain_policy(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<ExplainBody>,
) -> Response {
    match state
        .permissions
        .explain(
            &context,
            &body.entity,
            &body.action,
            body.field.as_deref(),
            body.record.as_ref(),
        )
        .await
    {
        Ok(explanation) => Json(json!({ "data": explanation })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new()
        .routes(routes!(list_users, create_user))
        .routes(routes!(assign_role))
        .routes(routes!(revoke_role))
        .routes(routes!(invalidate_context))
        .routes(routes!(list_policies, create_policy))
        .routes(routes!(seed_default_policies))
        .routes(routes!(sync_matrix_policies))
        .routes(routes!(explain_policy))
        .routes(routes!(delete_policy))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
