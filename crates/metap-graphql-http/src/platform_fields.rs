//! Hand-written GraphQL `Query`/`Mutation` fields for the 8 REST route groups that used to be
//! `metap-http`'s "deliberately not removed yet" backlog (`routes/{admin,cron,dashboards,
//! preferences,platform_config,tenant_config,users,oauth2}.rs` — see `../metap-docs/docs/roadmap/
//! 95-platform-graphql-fields.md`). Unlike entity CRUD (`metap-graphql/src/schema.rs`), none of
//! these resources are `MetadataRegistry`-registered `EntityDefinition`s — each uses its own
//! bespoke service (`metap_peripherals`, `PermissionService`, `metap_cron`, `metap_oauth_server`,
//! `metap_dashboards`, `metap_config`), so there is no dynamic-schema-from-metadata seam to reuse.
//! These fields are added onto the same `Query`/`Mutation` `Object`s the entity loop builds,
//! via `metap_graphql::build_schema_parts`'s documented extension point — see this crate's
//! `SchemaHolder::build`.
//!
//! Lives here, not in `metap-graphql`, because every resolver needs `metap_http::AppState`
//! (`state.pool`/`state.router`/`state.permissions`/`state.config`/
//! `state.context_attributes_cache`) — `metap-graphql` itself must stay backend-agnostic (only
//! `RecordBackend`/`MetadataRegistry`).
//!
//! **Every non-scalar output is a real, named GraphQL object type** (`AdminUserSummary`/
//! `Policy`/`CronJob`/`CronJobRun`/`OAuthClient`/`DashboardConfig`/`Preferences`/
//! `PlatformConfigItem`/`TenantConfigItem`/`SetConfigResult`/`SetTenantConfigResult`), not a
//! `Json` scalar (2026-09-26, `../metap-docs/docs/roadmap/97-platform-fields-typed-objects.md`;
//! Phase 95 originally shipped these as `Json` — a deliberate, explicitly-flagged scope cut —
//! this phase closes it). Each type is a thin GraphQL view over the exact same `serde_json::Value`
//! every resolver already built, via [`JsonHandle`] — the same "wrap the already-serialized JSON,
//! resolve each field with a JSON-pointer lookup" pattern `metap-graphql/src/record_handle.rs`
//! established for entity records, applied here to non-entity data instead. A field whose value is
//! genuinely dynamic/schema-less by design (a cron trigger/target config that varies by type, a
//! dashboard layout, a config value, a policy condition tree, `explainPermission`'s diagnostic
//! trace) still uses the `Json` scalar — typing those would either be inaccurate (they're not one
//! fixed shape) or duplicate validation that already happens server-side; see each type's own
//! field list below for exactly which fields stayed `Json` and why.
//!
//! **Auth is per-field here, not per-route.** REST gated each handler with an axum extractor
//! (`AuthContext`/`AdminContext`/`PlatformAdminContext`) that ran before the handler at all — one
//! GraphQL schema has one blanket `AuthContext` gate at `POST /graphql`
//! (`metap-graphql-http::router_with_federation`) and nothing stronger. [`require_admin`]/
//! [`require_platform_admin`] below replicate `AdminContext`/`PlatformAdminContext`'s exact role
//! checks (`metap_http::auth`) inside the resolver itself — every field that REST gated with one
//! of those two extractors calls the matching helper first.

use async_graphql::dynamic::{
    Field, FieldFuture, FieldValue, InputObject, InputValue, Object, ResolverContext, SchemaBuilder, TypeRef,
};
use async_graphql::{Error as GqlError, Value as GqlValue};
use metap_http::AppState;
use metap_permission::RequestContext;
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

const JSON_SCALAR: &str = "Json";

// -------------------------------------------------------------------------------------------
// Shared helpers: context access, auth, error mapping, JSON object/scalar plumbing
// -------------------------------------------------------------------------------------------

fn state_from_ctx<'a>(ctx: &ResolverContext<'a>) -> &'a AppState {
    ctx.data_unchecked::<AppState>()
}

fn context_from_ctx<'a>(ctx: &ResolverContext<'a>) -> Result<&'a RequestContext, GqlError> {
    ctx.data::<RequestContext>()
}

fn gql_err(status: u16, code: &str, message: impl Into<String>) -> GqlError {
    let mut err = GqlError::new(message.into());
    let mut extensions = async_graphql::ErrorExtensionValues::default();
    extensions.set("code", code);
    extensions.set("status", status as i32);
    err.extensions = Some(extensions);
    err
}

fn anyhow_err(e: anyhow::Error) -> GqlError {
    gql_err(500, "internal", e.to_string())
}

fn validation_err(message: impl Into<String>) -> GqlError {
    gql_err(400, "validation_failed", message)
}

fn config_err(e: metap_config::ConfigError) -> GqlError {
    match e {
        metap_config::ConfigError::UnknownKey(key) => {
            gql_err(404, "unknown_config_key", format!("No config key named {key:?}."))
        }
        metap_config::ConfigError::NotWritable { key, reason } => gql_err(
            403,
            "config_key_not_writable",
            format!("Config key {key:?} cannot be set here: {reason}"),
        ),
        metap_config::ConfigError::Invalid { key, reason } => gql_err(
            422,
            "invalid_config_value",
            format!("Invalid value for {key:?}: {reason}"),
        ),
        metap_config::ConfigError::Db(e) => anyhow_err(e.into()),
    }
}

/// The caller's tenant, resolved the same way every REST handler here did
/// (`state.permissions.scoped_tenant`) — no role check, the GraphQL equivalent of `AuthContext`.
fn caller<'a>(ctx: &ResolverContext<'a>) -> Result<(&'a AppState, &'a RequestContext, Uuid), GqlError> {
    let state = state_from_ctx(ctx);
    let context = context_from_ctx(ctx)?;
    let tenant_id = state.permissions.scoped_tenant(context).map_err(anyhow_err)?;
    Ok((state, context, tenant_id))
}

/// Replicates `metap_http::auth::AdminContext`'s exact check (`context.is_admin()`) — every field
/// REST gated with `AdminContext` calls this first.
fn require_admin<'a>(ctx: &ResolverContext<'a>) -> Result<(&'a AppState, &'a RequestContext, Uuid), GqlError> {
    let (state, context, tenant_id) = caller(ctx)?;
    if !context.is_admin() {
        return Err(gql_err(403, "forbidden", "This action requires the admin role."));
    }
    Ok((state, context, tenant_id))
}

/// Replicates `metap_http::auth::PlatformAdminContext`'s exact check (platform tenant + the
/// `platform_admin` role) — every field REST gated with `PlatformAdminContext` calls this first.
fn require_platform_admin<'a>(ctx: &ResolverContext<'a>) -> Result<&'a AppState, GqlError> {
    let state = state_from_ctx(ctx);
    let context = context_from_ctx(ctx)?;
    let is_platform_tenant = context.tenant_id == metap_control::PLATFORM_TENANT_ID.to_string();
    let has_platform_admin_role = context
        .roles
        .as_ref()
        .is_some_and(|roles| roles.iter().any(|r| r == "platform_admin"));
    if !is_platform_tenant || !has_platform_admin_role {
        return Err(gql_err(
            403,
            "forbidden",
            "This action requires the platform_admin role.",
        ));
    }
    Ok(state)
}

fn user_id_of(context: &RequestContext) -> Result<Uuid, GqlError> {
    context
        .user_id
        .as_deref()
        .and_then(|s| Uuid::parse_str(s).ok())
        .ok_or_else(|| gql_err(401, "unauthorized", "Token is missing a user id."))
}

/// Wraps an already-built `serde_json::Value` object so a registered [`Object`] type's fields can
/// each resolve themselves with a plain JSON-pointer lookup — the exact same role
/// `metap-graphql/src/record_handle.rs`'s `RecordHandle` plays for entity records, applied here to
/// every non-entity type this module registers.
struct JsonHandle(Value);

/// A `Json`-scalar leaf value, or `null` for a missing/absent key.
fn json_field_value(value: Option<&Value>) -> Option<FieldValue<'static>> {
    match value {
        None | Some(Value::Null) => None,
        Some(v) => GqlValue::from_json(v.clone()).ok().map(FieldValue::value),
    }
}

/// Builds one field of a `JsonHandle`-backed `Object` type — reads `name` straight out of the
/// wrapped JSON object and converts it to whatever GraphQL scalar `type_ref` declares (string/int/
/// boolean/ID all round-trip through `async_graphql::Value::from_json` the same way the `Json`
/// scalar does; the declared `type_ref` is purely a schema/introspection promise, not a runtime
/// coercion this function performs itself — every value here already comes from a `serde_json`
/// serialization of a concrete Rust type, so it's already shaped correctly).
fn json_field(name: &'static str, type_ref: TypeRef) -> Field {
    Field::new(name, type_ref, move |ctx| {
        let handle = ctx
            .parent_value
            .downcast_ref::<JsonHandle>()
            .expect("parent_value is always a JsonHandle for platform_fields object types");
        FieldFuture::Value(json_field_value(handle.0.get(name)))
    })
}

/// One record of a registered `Object` type, ready to return from a field whose declared type is
/// that `Object` (not a list, not a union — no `.with_type()` needed, same reasoning
/// `RecordHandle::from_dto` doesn't need it for a non-federated entity field).
fn json_object(value: Value) -> FieldValue<'static> {
    FieldValue::owned_any(JsonHandle(value))
}

fn json_object_list(values: Vec<Value>) -> FieldValue<'static> {
    FieldValue::list(values.into_iter().map(json_object))
}

fn json_value(v: Value) -> FieldValue<'static> {
    match GqlValue::from_json(v) {
        Ok(v) => FieldValue::value(v),
        Err(_) => FieldValue::NULL,
    }
}

fn true_field() -> FieldValue<'static> {
    FieldValue::value(GqlValue::Boolean(true))
}

fn json_arg(ctx: &ResolverContext<'_>, name: &str) -> Result<Value, GqlError> {
    ctx.args
        .try_get(name)?
        .as_value()
        .clone()
        .into_json()
        .map_err(|e| validation_err(e.to_string()))
}

fn json_arg_opt(ctx: &ResolverContext<'_>, name: &str) -> Result<Option<Value>, GqlError> {
    match ctx.args.get(name) {
        Some(v) if !v.is_null() => Ok(Some(
            v.as_value()
                .clone()
                .into_json()
                .map_err(|e| validation_err(e.to_string()))?,
        )),
        _ => Ok(None),
    }
}

fn string_arg_opt(ctx: &ResolverContext<'_>, name: &str) -> Result<Option<String>, GqlError> {
    match ctx.args.get(name) {
        Some(v) if !v.is_null() => Ok(Some(v.string()?.to_string())),
        _ => Ok(None),
    }
}

fn string_list_arg_opt(ctx: &ResolverContext<'_>, name: &str) -> Result<Option<Vec<String>>, GqlError> {
    match ctx.args.get(name) {
        Some(v) if !v.is_null() => {
            let list = v.list()?;
            let mut out = Vec::with_capacity(list.len());
            for item in list.iter() {
                out.push(item.string()?.to_string());
            }
            Ok(Some(out))
        }
        _ => Ok(None),
    }
}

fn uuid_arg(ctx: &ResolverContext<'_>, name: &str) -> Result<Uuid, GqlError> {
    Uuid::parse_str(ctx.args.try_get(name)?.string()?).map_err(|e| validation_err(e.to_string()))
}

fn i64_arg_opt(ctx: &ResolverContext<'_>, name: &str) -> Option<i64> {
    ctx.args.get(name).and_then(|v| v.i64().ok())
}

// -------------------------------------------------------------------------------------------
// Object types — each a thin JsonHandle view (see module doc comment). A field typed `Json` here
// stays that way deliberately: the underlying value has no single fixed shape (varies by
// triggerType/targetType, is an opaque per-tenant blob, or is a recursive condition tree).
// -------------------------------------------------------------------------------------------

fn admin_user_summary_object() -> Object {
    Object::new("AdminUserSummary")
        .field(json_field("userId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("roles", TypeRef::named_nn_list_nn(TypeRef::STRING)))
}

fn create_admin_user_result_object() -> Object {
    Object::new("CreateAdminUserResult")
        .field(json_field("userId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("email", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("roles", TypeRef::named_nn_list_nn(TypeRef::STRING)))
}

fn tenant_user_summary_object() -> Object {
    Object::new("TenantUserSummary")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("email", TypeRef::named_nn(TypeRef::STRING)))
}

fn policy_object() -> Object {
    Object::new("Policy")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("tenantId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("entity", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("action", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("field", TypeRef::named(TypeRef::STRING)))
        .field(json_field("subject", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("roles", TypeRef::named_list_nn(TypeRef::STRING)))
        // A recursive condition tree (`PolicyCondition`) — genuinely schema-less from GraphQL's
        // point of view, same reasoning `createPolicy`'s `condition` argument stays `Json`.
        .field(json_field("condition", TypeRef::named(JSON_SCALAR)))
        .field(json_field("createdBy", TypeRef::named(TypeRef::ID)))
        .field(json_field("effect", TypeRef::named_nn(TypeRef::STRING)))
}

fn cron_job_object() -> Object {
    Object::new("CronJob")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("tenantId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("name", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("enabled", TypeRef::named_nn(TypeRef::BOOLEAN)))
        .field(json_field("triggerType", TypeRef::named_nn(TypeRef::STRING)))
        // Shape depends on `triggerType` (`{entity,action}` for on_transition, `{entity,event}`
        // for on_record_event, absent for schedule) — see `validate_trigger` below.
        .field(json_field("triggerConfig", TypeRef::named(JSON_SCALAR)))
        .field(json_field("cronExpr", TypeRef::named(TypeRef::STRING)))
        .field(json_field("timezone", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("targetType", TypeRef::named_nn(TypeRef::STRING)))
        // Shape depends on `targetType` (5 different payload shapes, or a `steps` chain of them)
        // — see `validate_target_config` below.
        .field(json_field("targetConfig", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("dispatchMode", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("maxAttempts", TypeRef::named_nn(TypeRef::INT)))
        .field(json_field("retryBackoffSeconds", TypeRef::named_nn(TypeRef::INT)))
        .field(json_field("nextRunAt", TypeRef::named(TypeRef::STRING)))
        .field(json_field("lastRunAt", TypeRef::named(TypeRef::STRING)))
        .field(json_field("createdAt", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("updatedAt", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("createdBy", TypeRef::named(TypeRef::ID)))
}

fn cron_job_run_object() -> Object {
    Object::new("CronJobRun")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("tenantId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("jobId", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("status", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("attempt", TypeRef::named_nn(TypeRef::INT)))
        .field(json_field("scheduledFor", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("startedAt", TypeRef::named(TypeRef::STRING)))
        .field(json_field("finishedAt", TypeRef::named(TypeRef::STRING)))
        .field(json_field("error", TypeRef::named(TypeRef::STRING)))
        // Free-form per dispatch target (webhook response body, email send result, ...).
        .field(json_field("responseSummary", TypeRef::named(JSON_SCALAR)))
        .field(json_field("createdAt", TypeRef::named_nn(TypeRef::STRING)))
}

fn oauth_client_object() -> Object {
    Object::new("OAuthClient")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("clientId", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("name", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("redirectUris", TypeRef::named_nn_list_nn(TypeRef::STRING)))
        .field(json_field("allowedScopes", TypeRef::named_nn_list_nn(TypeRef::STRING)))
        .field(json_field("isConfidential", TypeRef::named_nn(TypeRef::BOOLEAN)))
        .field(json_field("serviceUserId", TypeRef::named(TypeRef::ID)))
        // Only ever non-null once, in `createOAuthClient`'s own result — see that mutation's doc
        // comment (write-once, same discipline `metap-oauth-server::create_client` documents).
        .field(json_field("clientSecret", TypeRef::named(TypeRef::STRING)))
}

fn dashboard_config_object() -> Object {
    Object::new("DashboardConfig")
        .field(json_field("id", TypeRef::named_nn(TypeRef::ID)))
        .field(json_field("ownerUserId", TypeRef::named(TypeRef::ID)))
        // A dashboard layout is an opaque JSON blob to this crate and to `metap-dashboards`
        // itself, interpreted only by the frontend's widget catalog — see that crate's own doc
        // comment.
        .field(json_field("layout", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("updatedAt", TypeRef::named_nn(TypeRef::STRING)))
}

fn preferences_object() -> Object {
    Object::new("Preferences").field(json_field("locale", TypeRef::named_nn(TypeRef::STRING)))
}

fn platform_config_item_object() -> Object {
    Object::new("PlatformConfigItem")
        .field(json_field("key", TypeRef::named_nn(TypeRef::STRING)))
        // A config value's shape is whatever that specific key's declaration says (string, bool,
        // number, or a structured object for some keys) — genuinely per-key, not one fixed shape.
        .field(json_field("value", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("level", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("tenantOverridable", TypeRef::named_nn(TypeRef::BOOLEAN)))
}

fn tenant_config_item_object() -> Object {
    Object::new("TenantConfigItem")
        .field(json_field("key", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("value", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("level", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("overridden", TypeRef::named_nn(TypeRef::BOOLEAN)))
        .field(json_field("public", TypeRef::named_nn(TypeRef::BOOLEAN)))
}

fn set_config_result_object() -> Object {
    Object::new("SetConfigResult")
        .field(json_field("key", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("value", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("appliesImmediately", TypeRef::named_nn(TypeRef::BOOLEAN)))
}

fn set_tenant_config_result_object() -> Object {
    Object::new("SetTenantConfigResult")
        .field(json_field("key", TypeRef::named_nn(TypeRef::STRING)))
        .field(json_field("value", TypeRef::named_nn(JSON_SCALAR)))
        .field(json_field("overridden", TypeRef::named_nn(TypeRef::BOOLEAN)))
}

// -------------------------------------------------------------------------------------------
// Input types. `CronJobInput`/`CronJobUpdateInput`'s `triggerConfig`/`targetConfig` stay `Json`
// for the same reason the matching `CronJob` output fields do — the resolver still fully
// validates them (`validate_trigger`/`validate_target_config` below), a typed GraphQL schema
// would only duplicate that validation, not replace it.
// -------------------------------------------------------------------------------------------

fn matrix_grant_input_object() -> InputObject {
    InputObject::new("MatrixGrantInput")
        .field(InputValue::new("role", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("action", TypeRef::named_nn(TypeRef::STRING)))
}

fn cron_job_input_object() -> InputObject {
    InputObject::new("CronJobInput")
        .field(InputValue::new("name", TypeRef::named_nn(TypeRef::STRING)))
        .field(InputValue::new("triggerType", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("triggerConfig", TypeRef::named(JSON_SCALAR)))
        .field(InputValue::new("cronExpr", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("timezone", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("targetType", TypeRef::named_nn(TypeRef::STRING)))
        .field(InputValue::new("targetConfig", TypeRef::named_nn(JSON_SCALAR)))
        .field(InputValue::new("dispatchMode", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("maxAttempts", TypeRef::named(TypeRef::INT)))
        .field(InputValue::new("retryBackoffSeconds", TypeRef::named(TypeRef::INT)))
        .field(InputValue::new("enabled", TypeRef::named(TypeRef::BOOLEAN)))
}

fn cron_job_update_input_object() -> InputObject {
    InputObject::new("CronJobUpdateInput")
        .field(InputValue::new("name", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("triggerType", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("triggerConfig", TypeRef::named(JSON_SCALAR)))
        .field(InputValue::new("cronExpr", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("timezone", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("targetType", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("targetConfig", TypeRef::named(JSON_SCALAR)))
        .field(InputValue::new("dispatchMode", TypeRef::named(TypeRef::STRING)))
        .field(InputValue::new("maxAttempts", TypeRef::named(TypeRef::INT)))
        .field(InputValue::new("retryBackoffSeconds", TypeRef::named(TypeRef::INT)))
        .field(InputValue::new("enabled", TypeRef::named(TypeRef::BOOLEAN)))
}

// -------------------------------------------------------------------------------------------
// Group 1: users / roles (`routes/admin.rs`, `routes/users.rs`)
// -------------------------------------------------------------------------------------------

fn user_roles_to_json(u: &metap_peripherals::UserRoles) -> Value {
    json!({ "userId": u.user_id, "roles": u.roles })
}

fn policy_to_json(row: &metap_permission::PolicyRow) -> Value {
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

fn client_to_json(client: &metap_oauth_server::OAuthClient) -> Value {
    json!({
        "id": client.id,
        "clientId": client.client_id,
        "name": client.name,
        "redirectUris": client.redirect_uris,
        "allowedScopes": client.allowed_scopes,
        "isConfidential": client.is_confidential,
        "serviceUserId": client.service_user_id,
    })
}

fn dashboard_to_json(config: &metap_dashboards::DashboardConfig) -> Value {
    json!({
        "id": config.id,
        "ownerUserId": config.owner_user_id,
        "layout": config.layout,
        "updatedAt": config.updated_at,
    })
}

// -------------------------------------------------------------------------------------------
// Group 3: cron jobs (`routes/cron.rs`) — validation ported verbatim (return type only differs:
// `GqlError` instead of `Box<Response>`), since `metap_cron::create_job`/`update_job` themselves
// perform none of it.
// -------------------------------------------------------------------------------------------

fn default_trigger_type() -> String {
    metap_cron::TriggerType::Schedule.as_str().to_string()
}
fn default_timezone() -> String {
    "UTC".to_string()
}
fn default_dispatch_mode() -> String {
    metap_cron::DispatchMode::Outbox.as_str().to_string()
}
fn default_max_attempts() -> i32 {
    1
}
fn default_retry_backoff_seconds() -> i32 {
    30
}
fn default_true() -> bool {
    true
}

#[derive(Deserialize)]
struct CreateCronJobInput {
    name: String,
    #[serde(rename = "triggerType", default = "default_trigger_type")]
    trigger_type: String,
    #[serde(rename = "triggerConfig")]
    trigger_config: Option<Value>,
    #[serde(rename = "cronExpr")]
    cron_expr: Option<String>,
    #[serde(default = "default_timezone")]
    timezone: String,
    #[serde(rename = "targetType")]
    target_type: String,
    #[serde(rename = "targetConfig")]
    target_config: Value,
    #[serde(rename = "dispatchMode", default = "default_dispatch_mode")]
    dispatch_mode: String,
    #[serde(rename = "maxAttempts", default = "default_max_attempts")]
    max_attempts: i32,
    #[serde(rename = "retryBackoffSeconds", default = "default_retry_backoff_seconds")]
    retry_backoff_seconds: i32,
    #[serde(default = "default_true")]
    enabled: bool,
}

#[derive(Deserialize, Default)]
struct UpdateCronJobInput {
    name: Option<String>,
    #[serde(rename = "triggerType")]
    trigger_type: Option<String>,
    #[serde(rename = "triggerConfig")]
    trigger_config: Option<Value>,
    #[serde(rename = "cronExpr")]
    cron_expr: Option<String>,
    timezone: Option<String>,
    #[serde(rename = "targetType")]
    target_type: Option<String>,
    #[serde(rename = "targetConfig")]
    target_config: Option<Value>,
    #[serde(rename = "dispatchMode")]
    dispatch_mode: Option<String>,
    #[serde(rename = "maxAttempts")]
    max_attempts: Option<i32>,
    #[serde(rename = "retryBackoffSeconds")]
    retry_backoff_seconds: Option<i32>,
    enabled: Option<bool>,
}

#[derive(Deserialize)]
struct StepActivityInput {
    #[serde(rename = "targetType")]
    target_type: String,
    #[serde(rename = "targetConfig", default)]
    target_config: Value,
}

fn validate_trigger(
    trigger_type: &str,
    trigger_config: Option<&Value>,
    cron_expr: Option<&str>,
    timezone: &str,
) -> Result<(), GqlError> {
    match metap_cron::TriggerType::parse(trigger_type) {
        Some(metap_cron::TriggerType::Schedule) => {
            let Some(cron_expr) = cron_expr else {
                return Err(validation_err(
                    "`cronExpr` is required when `triggerType` is \"schedule\".",
                ));
            };
            metap_cron::validate_schedule(cron_expr, timezone).map_err(|e| validation_err(e.to_string()))
        }
        Some(metap_cron::TriggerType::OnTransition) => {
            let Some(trigger_config) = trigger_config else {
                return Err(validation_err(
                    "`triggerConfig` ({entity, action}) is required when `triggerType` is \"on_transition\".",
                ));
            };
            let cfg: metap_cron::OnTransitionTriggerConfig = serde_json::from_value(trigger_config.clone())
                .map_err(|_| validation_err("`triggerConfig` must be `{ entity: string, action: string }`."))?;
            if cfg.entity.trim().is_empty() || cfg.action.trim().is_empty() {
                return Err(validation_err(
                    "`triggerConfig.entity`/`triggerConfig.action` must not be empty.",
                ));
            }
            Ok(())
        }
        Some(metap_cron::TriggerType::OnRecordEvent) => {
            let Some(trigger_config) = trigger_config else {
                return Err(validation_err(
                    "`triggerConfig` ({entity, event}) is required when `triggerType` is \"on_record_event\".",
                ));
            };
            let cfg: metap_cron::OnRecordEventTriggerConfig = serde_json::from_value(trigger_config.clone())
                .map_err(|_| validation_err("`triggerConfig` must be `{ entity: string, event: string }`."))?;
            if cfg.entity.trim().is_empty() {
                return Err(validation_err("`triggerConfig.entity` must not be empty."));
            }
            if !matches!(cfg.event.as_str(), "created" | "updated" | "deleted") {
                return Err(validation_err(
                    "`triggerConfig.event` must be one of: created, updated, deleted.",
                ));
            }
            Ok(())
        }
        None => Err(validation_err(
            "`triggerType` must be one of: schedule, on_transition, on_record_event.",
        )),
    }
}

fn validate_wait_event_config(index: usize, target_config: &Value) -> Result<(), GqlError> {
    let cfg: metap_cron::WaitEventTargetConfig = serde_json::from_value(target_config.clone()).map_err(|_| {
        validation_err(format!(
            "`targetConfig.steps[{index}].targetConfig` must be `{{ entity: string, action?: string, event?: string }}`."
        ))
    })?;
    if cfg.entity.trim().is_empty() {
        return Err(validation_err(format!(
            "`targetConfig.steps[{index}].targetConfig.entity` must not be empty."
        )));
    }
    match (&cfg.action, &cfg.event) {
        (Some(action), None) if !action.trim().is_empty() => Ok(()),
        (None, Some(event)) if matches!(event.as_str(), "created" | "updated" | "deleted") => Ok(()),
        (None, Some(_)) => Err(validation_err(format!(
            "`targetConfig.steps[{index}].targetConfig.event` must be one of: created, updated, deleted."
        ))),
        _ => Err(validation_err(format!(
            "`targetConfig.steps[{index}].targetConfig` must set exactly one of `action`/`event`."
        ))),
    }
}

fn validate_target_config(target_type: &str, target_config: &Value) -> Result<(), GqlError> {
    if metap_cron::TargetType::parse(target_type) == Some(metap_cron::TargetType::WaitEvent) {
        return Err(validation_err(
            "`targetType` \"wait_event\" is only valid as a step inside a \"steps\" chain, not as a job's own targetType.",
        ));
    }
    if metap_cron::TargetType::parse(target_type) != Some(metap_cron::TargetType::Steps) {
        return Ok(());
    }
    let Some(steps) = target_config.get("steps").and_then(Value::as_array) else {
        return Err(validation_err(
            "`targetConfig.steps` (a non-empty array) is required when `targetType` is \"steps\".",
        ));
    };
    if steps.is_empty() {
        return Err(validation_err("`targetConfig.steps` must not be empty."));
    }
    for (index, step) in steps.iter().enumerate() {
        let activity: StepActivityInput = serde_json::from_value(step.clone()).map_err(|_| {
            validation_err(format!(
                "`targetConfig.steps[{index}]` must be `{{ targetType: string, targetConfig: object }}`."
            ))
        })?;
        match metap_cron::TargetType::parse(&activity.target_type) {
            Some(metap_cron::TargetType::Steps) => {
                return Err(validation_err(format!(
                    "`targetConfig.steps[{index}].targetType` cannot be \"steps\" (chains cannot nest)."
                )));
            }
            Some(metap_cron::TargetType::WaitEvent) => validate_wait_event_config(index, &activity.target_config)?,
            None => {
                return Err(validation_err(format!(
                    "`targetConfig.steps[{index}].targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, wait_event."
                )));
            }
            Some(_) => {}
        }
    }
    Ok(())
}

fn not_found_cron_job() -> GqlError {
    gql_err(404, "cron_job_not_found", "Cron job not found.")
}

// -------------------------------------------------------------------------------------------
// The extension point: called from `SchemaHolder::build` on the `(builder, query, mutation)`
// triple `metap_graphql::build_schema_parts`/`build_schema_parts_with_federation` returns, before
// `.register(query).register(mutation).finish()`.
// -------------------------------------------------------------------------------------------

pub fn add_platform_fields(
    mut builder: SchemaBuilder,
    mut query: Object,
    mut mutation: Object,
) -> (SchemaBuilder, Object, Object) {
    builder = builder
        .register(admin_user_summary_object())
        .register(create_admin_user_result_object())
        .register(tenant_user_summary_object())
        .register(policy_object())
        .register(cron_job_object())
        .register(cron_job_run_object())
        .register(oauth_client_object())
        .register(dashboard_config_object())
        .register(preferences_object())
        .register(platform_config_item_object())
        .register(tenant_config_item_object())
        .register(set_config_result_object())
        .register(set_tenant_config_result_object())
        .register(matrix_grant_input_object())
        .register(cron_job_input_object())
        .register(cron_job_update_input_object());

    // --- Query: users/roles -----------------------------------------------------------------
    query = query.field(Field::new(
        "adminUsers",
        TypeRef::named_nn_list_nn("AdminUserSummary"),
        |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let users = metap_peripherals::list_users(&mut *tx, tenant_id)
                    .await
                    .map_err(anyhow_err)?;
                let _ = tx.commit().await;
                Ok(Some(json_object_list(users.iter().map(user_roles_to_json).collect())))
            })
        },
    ));

    query = query.field(Field::new(
        "tenantUsers",
        TypeRef::named_nn_list_nn("TenantUserSummary"),
        |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = caller(&ctx)?;
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let users = metap_peripherals::list_tenant_users(&mut *tx, tenant_id)
                    .await
                    .map_err(anyhow_err)?;
                let _ = tx.commit().await;
                let data: Vec<Value> = users
                    .into_iter()
                    .map(|u| json!({"id": u.id, "email": u.email}))
                    .collect();
                Ok(Some(json_object_list(data)))
            })
        },
    ));

    // --- Query: policies ---------------------------------------------------------------------
    query = query.field(
        Field::new("policies", TypeRef::named_nn_list_nn("Policy"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let entity = string_arg_opt(&ctx, "entity")?;
                let rows = state
                    .permissions
                    .list_policies(tenant_id, entity.as_deref())
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object_list(rows.iter().map(policy_to_json).collect())))
            })
        })
        .argument(InputValue::new("entity", TypeRef::named(TypeRef::STRING))),
    );

    // --- Query: cron jobs --------------------------------------------------------------------
    query = query.field(Field::new("cronJobs", TypeRef::named_nn_list_nn("CronJob"), |ctx| {
        FieldFuture::new(async move {
            let (state, _context, tenant_id) = require_admin(&ctx)?;
            let jobs = metap_cron::list_jobs(&state.pool, tenant_id)
                .await
                .map_err(anyhow_err)?;
            let data: Vec<Value> = jobs
                .iter()
                .map(|j| serde_json::to_value(j).unwrap_or(Value::Null))
                .collect();
            Ok(Some(json_object_list(data)))
        })
    }));

    query = query.field(
        Field::new("cronJob", TypeRef::named("CronJob"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                match metap_cron::get_job(&state.pool, tenant_id, id)
                    .await
                    .map_err(anyhow_err)?
                {
                    Some(job) => Ok(Some(json_object(serde_json::to_value(&job).unwrap_or(Value::Null)))),
                    None => Ok(None),
                }
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID))),
    );

    query = query.field(
        Field::new("cronJobRuns", TypeRef::named_nn_list_nn("CronJobRun"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                let limit = i64_arg_opt(&ctx, "limit").filter(|n| *n > 0 && *n <= 200).unwrap_or(50);
                let runs = metap_cron::list_job_runs(&state.pool, tenant_id, id, limit)
                    .await
                    .map_err(anyhow_err)?;
                let data: Vec<Value> = runs
                    .iter()
                    .map(|r| serde_json::to_value(r).unwrap_or(Value::Null))
                    .collect();
                Ok(Some(json_object_list(data)))
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("limit", TypeRef::named(TypeRef::INT))),
    );

    // Step-level progress for one `TargetType::Steps` firing — kept as `Json`, not a typed
    // object: `WorkflowRun`'s own shape belongs to `metap-workflow`, not this module, and this
    // field has zero real frontend consumers today (confirmed by an org-wide grep before Phase
    // 95 shipped) — not worth a dedicated type until something actually needs one.
    query = query.field(
        Field::new("cronJobWorkflowRun", TypeRef::named(JSON_SCALAR), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let job_id = uuid_arg(&ctx, "jobId")?;
                let run_id = uuid_arg(&ctx, "runId")?;
                match metap_cron::get_workflow_run_by_cron_job_run(&state.pool, tenant_id, run_id)
                    .await
                    .map_err(anyhow_err)?
                {
                    Some(run) if run.job_id == job_id => {
                        Ok(Some(json_value(serde_json::to_value(&run).unwrap_or(Value::Null))))
                    }
                    _ => Err(gql_err(404, "workflow_run_not_found", "Workflow run not found.")),
                }
            })
        })
        .argument(InputValue::new("jobId", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("runId", TypeRef::named_nn(TypeRef::ID))),
    );

    // --- Query: oauth admin clients ------------------------------------------------------------
    query = query.field(Field::new(
        "oauthClients",
        TypeRef::named_nn_list_nn("OAuthClient"),
        |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let clients = metap_oauth_server::list_clients(&state.pool, tenant_id)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object_list(clients.iter().map(client_to_json).collect())))
            })
        },
    ));

    // --- Query: dashboards ---------------------------------------------------------------------
    query = query.field(Field::new("myDashboard", TypeRef::named("DashboardConfig"), |ctx| {
        FieldFuture::new(async move {
            let (state, context, tenant_id) = caller(&ctx)?;
            let user_id = user_id_of(context)?;
            let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
            let config = metap_dashboards::get_effective_dashboard(&mut tx, tenant_id, user_id)
                .await
                .map_err(anyhow_err)?;
            let _ = tx.commit().await;
            Ok(config.as_ref().map(dashboard_to_json).map(json_object))
        })
    }));

    query = query.field(Field::new(
        "tenantDefaultDashboard",
        TypeRef::named("DashboardConfig"),
        |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = caller(&ctx)?;
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let config = metap_dashboards::get_tenant_default(&mut *tx, tenant_id)
                    .await
                    .map_err(anyhow_err)?;
                let _ = tx.commit().await;
                Ok(config.as_ref().map(dashboard_to_json).map(json_object))
            })
        },
    ));

    // --- Query: preferences ----------------------------------------------------------------------
    query = query.field(Field::new("myPreferences", TypeRef::named_nn("Preferences"), |ctx| {
        FieldFuture::new(async move {
            let (state, context, tenant_id) = caller(&ctx)?;
            let user_id = user_id_of(context)?;
            let locale = metap_peripherals::get_locale(&state.pool, tenant_id, user_id)
                .await
                .map_err(anyhow_err)?;
            Ok(Some(json_object(json!({ "locale": locale }))))
        })
    }));

    // --- Query: platform/tenant config -------------------------------------------------------
    query = query.field(Field::new(
        "platformConfig",
        TypeRef::named_nn_list_nn("PlatformConfigItem"),
        |ctx| {
            FieldFuture::new(async move {
                let state = require_platform_admin(&ctx)?;
                let snapshot = state.config.current();
                let items: Vec<Value> = snapshot
                    .platform_writable_view()
                    .into_iter()
                    .map(|(def, value)| {
                        json!({
                            "key": def.key,
                            "value": value,
                            "level": level_name(def.level),
                            "tenantOverridable": def.level == metap_config::ConfigLevel::Tenant,
                        })
                    })
                    .collect();
                Ok(Some(json_object_list(items)))
            })
        },
    ));

    query = query.field(Field::new(
        "tenantConfig",
        TypeRef::named_nn_list_nn("TenantConfigItem"),
        |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = caller(&ctx)?;
                let effective = state.effective_config(tenant_id).await;
                let items: Vec<Value> = effective
                    .tenant_view()
                    .into_iter()
                    .map(|(def, value, overridden)| {
                        json!({
                            "key": def.key,
                            "value": value,
                            "level": level_name(def.level),
                            "overridden": overridden,
                            "public": def.public,
                        })
                    })
                    .collect();
                Ok(Some(json_object_list(items)))
            })
        },
    ));

    // --- Mutation: users/roles -----------------------------------------------------------------
    mutation = mutation.field(
        Field::new("createAdminUser", TypeRef::named_nn("CreateAdminUserResult"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let email = ctx.args.try_get("email")?.string()?.to_string();
                let password = ctx.args.try_get("password")?.string()?.to_string();
                let roles = string_list_arg_opt(&ctx, "roles")?.unwrap_or_default();
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let user = match metap_peripherals::create_user(&mut *tx, tenant_id, &email, &password).await {
                    Ok(user) => user,
                    Err(e) => {
                        let is_duplicate_email = e
                            .downcast_ref::<sqlx::Error>()
                            .and_then(|e| e.as_database_error())
                            .is_some_and(|e| e.is_unique_violation());
                        if is_duplicate_email {
                            return Err(gql_err(409, "email_taken", "A user with this email already exists."));
                        }
                        return Err(anyhow_err(e));
                    }
                };
                let assigned_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                for role in &roles {
                    metap_peripherals::assign_role(&mut *tx, tenant_id, user.id, role, assigned_by)
                        .await
                        .map_err(anyhow_err)?;
                }
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                Ok(Some(json_object(
                    json!({ "userId": user.id, "email": user.email, "roles": roles }),
                )))
            })
        })
        .argument(InputValue::new("email", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("password", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("roles", TypeRef::named_nn_list(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new("assignUserRole", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let user_id = uuid_arg(&ctx, "userId")?;
                let role = ctx.args.try_get("role")?.string()?.to_string();
                let assigned_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                metap_peripherals::assign_role(&mut *tx, tenant_id, user_id, &role, assigned_by)
                    .await
                    .map_err(anyhow_err)?;
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                Ok(Some(true_field()))
            })
        })
        .argument(InputValue::new("userId", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("role", TypeRef::named_nn(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new("revokeUserRole", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let user_id = uuid_arg(&ctx, "userId")?;
                let role = ctx.args.try_get("role")?.string()?.to_string();
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                metap_peripherals::revoke_role(&mut *tx, tenant_id, user_id, &role)
                    .await
                    .map_err(anyhow_err)?;
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                Ok(Some(true_field()))
            })
        })
        .argument(InputValue::new("userId", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("role", TypeRef::named_nn(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new(
            "invalidateUserContextCache",
            TypeRef::named_nn(TypeRef::BOOLEAN),
            |ctx| {
                FieldFuture::new(async move {
                    let (state, _context, tenant_id) = require_admin(&ctx)?;
                    let user_id = uuid_arg(&ctx, "userId")?;
                    state.context_attributes_cache.invalidate(tenant_id, user_id).await;
                    Ok(Some(true_field()))
                })
            },
        )
        .argument(InputValue::new("userId", TypeRef::named_nn(TypeRef::ID))),
    );

    // --- Mutation: policies --------------------------------------------------------------------
    mutation = mutation.field(
        Field::new("createPolicy", TypeRef::named_nn("Policy"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let entity = ctx.args.try_get("entity")?.string()?.to_string();
                let action = ctx.args.try_get("action")?.string()?.to_string();
                let roles = string_list_arg_opt(&ctx, "roles")?;
                let condition: Option<metap_permission::PolicyCondition> = json_arg_opt(&ctx, "condition")?
                    .map(serde_json::from_value)
                    .transpose()
                    .map_err(|e: serde_json::Error| validation_err(e.to_string()))?;
                let field = string_arg_opt(&ctx, "field")?;
                let subject_str = string_arg_opt(&ctx, "subject")?;
                let subject = match subject_str.as_deref() {
                    Some("record") => metap_permission::PolicySubject::Record,
                    _ => metap_permission::PolicySubject::Context,
                };
                let effect_str = string_arg_opt(&ctx, "effect")?;
                let effect = effect_str
                    .as_deref()
                    .map(metap_permission::PolicyEffect::parse)
                    .unwrap_or(metap_permission::PolicyEffect::Allow);
                let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                let row = state
                    .permissions
                    .create_policy(
                        tenant_id,
                        &entity,
                        &action,
                        roles,
                        condition,
                        created_by,
                        field.as_deref(),
                        Some(subject),
                        effect,
                    )
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object(policy_to_json(&row))))
            })
        })
        .argument(InputValue::new("entity", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("action", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("roles", TypeRef::named_nn_list(TypeRef::STRING)))
        .argument(InputValue::new("condition", TypeRef::named(JSON_SCALAR)))
        .argument(InputValue::new("field", TypeRef::named(TypeRef::STRING)))
        .argument(InputValue::new("subject", TypeRef::named(TypeRef::STRING)))
        .argument(InputValue::new("effect", TypeRef::named(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new("deletePolicy", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                state
                    .permissions
                    .delete_policy(tenant_id, id)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(true_field()))
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID))),
    );

    const KNOWN_ACTIONS: [&str; 5] = [
        metap_permission::EntityAction::Read.as_str(),
        metap_permission::EntityAction::Create.as_str(),
        metap_permission::EntityAction::Update.as_str(),
        metap_permission::EntityAction::Delete.as_str(),
        metap_permission::EntityAction::Transition.as_str(),
    ];

    mutation = mutation.field(
        Field::new("seedDefaultPolicies", TypeRef::named_nn_list_nn("Policy"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let entity = ctx.args.try_get("entity")?.string()?.to_string();
                let roles = string_list_arg_opt(&ctx, "roles")?.unwrap_or_default();
                if roles.is_empty() {
                    return Err(validation_err("`roles` must not be empty."));
                }
                let actions_arg = string_list_arg_opt(&ctx, "actions")?.unwrap_or_default();
                let actions: Vec<&str> = if actions_arg.is_empty() {
                    KNOWN_ACTIONS.to_vec()
                } else {
                    actions_arg.iter().map(String::as_str).collect()
                };
                if let Some(unknown) = actions.iter().find(|a| !KNOWN_ACTIONS.contains(a)) {
                    return Err(validation_err(format!(
                        "Unknown action \"{unknown}\" — must be one of {KNOWN_ACTIONS:?}."
                    )));
                }
                let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                let mut created = Vec::with_capacity(actions.len());
                for action in actions {
                    let row = state
                        .permissions
                        .create_policy(
                            tenant_id,
                            &entity,
                            action,
                            Some(roles.clone()),
                            None,
                            created_by,
                            None,
                            Some(metap_permission::PolicySubject::Context),
                            metap_permission::PolicyEffect::Allow,
                        )
                        .await
                        .map_err(anyhow_err)?;
                    created.push(policy_to_json(&row));
                }
                Ok(Some(json_object_list(created)))
            })
        })
        .argument(InputValue::new("entity", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("roles", TypeRef::named_nn_list_nn(TypeRef::STRING)))
        .argument(InputValue::new("actions", TypeRef::named_nn_list(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new("syncPolicyMatrix", TypeRef::named_nn_list_nn("Policy"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let entity = ctx.args.try_get("entity")?.string()?.to_string();
                let grants_json = json_arg(&ctx, "grants")?;
                #[derive(Deserialize)]
                struct MatrixGrant {
                    role: Option<String>,
                    action: String,
                }
                let grants: Vec<MatrixGrant> =
                    serde_json::from_value(grants_json).map_err(|e| validation_err(e.to_string()))?;
                if let Some(unknown) = grants.iter().find(|g| !KNOWN_ACTIONS.contains(&g.action.as_str())) {
                    return Err(validation_err(format!(
                        "Unknown action \"{}\" — must be one of {KNOWN_ACTIONS:?}.",
                        unknown.action
                    )));
                }
                let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                let grants: Vec<(Option<String>, String)> = grants.into_iter().map(|g| (g.role, g.action)).collect();
                let rows = state
                    .permissions
                    .sync_basic_policies(tenant_id, &entity, grants, created_by)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object_list(rows.iter().map(policy_to_json).collect())))
            })
        })
        .argument(InputValue::new("entity", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("grants", TypeRef::named_nn_list_nn("MatrixGrantInput"))),
    );

    // `PolicyExplanation`/`PolicyTraceEntry` (`metap-permission`) don't derive `ToSchema` either
    // (REST's own doc comment for this route says the same) — a diagnostic trace shape, kept
    // `Json` rather than modeled, same call this module made for `cronJobWorkflowRun`.
    mutation = mutation.field(
        Field::new("explainPermission", TypeRef::named_nn(JSON_SCALAR), |ctx| {
            FieldFuture::new(async move {
                let context = context_from_ctx(&ctx)?;
                if !context.is_admin() {
                    return Err(gql_err(403, "forbidden", "This action requires the admin role."));
                }
                let state = state_from_ctx(&ctx);
                let entity = ctx.args.try_get("entity")?.string()?.to_string();
                let action = ctx.args.try_get("action")?.string()?.to_string();
                let field = string_arg_opt(&ctx, "field")?;
                let record: Option<serde_json::Map<String, Value>> =
                    json_arg_opt(&ctx, "record")?.and_then(|v| v.as_object().cloned());
                let explanation = state
                    .permissions
                    .explain(context, &entity, &action, field.as_deref(), record.as_ref())
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_value(
                    serde_json::to_value(&explanation).unwrap_or(Value::Null),
                )))
            })
        })
        .argument(InputValue::new("entity", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("action", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("field", TypeRef::named(TypeRef::STRING)))
        .argument(InputValue::new("record", TypeRef::named(JSON_SCALAR))),
    );

    // --- Mutation: cron jobs -------------------------------------------------------------------
    mutation = mutation.field(
        Field::new("createCronJob", TypeRef::named_nn("CronJob"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = require_admin(&ctx)?;
                let raw = json_arg(&ctx, "input")?;
                let body: CreateCronJobInput =
                    serde_json::from_value(raw).map_err(|e| validation_err(e.to_string()))?;
                if metap_cron::TargetType::parse(&body.target_type).is_none() {
                    return Err(validation_err(
                        "`targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, steps.",
                    ));
                }
                validate_target_config(&body.target_type, &body.target_config)?;
                if metap_cron::DispatchMode::parse(&body.dispatch_mode).is_none() {
                    return Err(validation_err("`dispatchMode` must be one of: outbox, direct."));
                }
                if body.max_attempts < 1 {
                    return Err(validation_err("`maxAttempts` must be at least 1."));
                }
                if body.retry_backoff_seconds < 0 {
                    return Err(validation_err("`retryBackoffSeconds` must be non-negative."));
                }
                validate_trigger(
                    &body.trigger_type,
                    body.trigger_config.as_ref(),
                    body.cron_expr.as_deref(),
                    &body.timezone,
                )?;
                let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
                let input = metap_cron::NewCronJob {
                    name: body.name,
                    trigger_type: body.trigger_type,
                    trigger_config: body.trigger_config,
                    cron_expr: body.cron_expr,
                    timezone: body.timezone,
                    target_type: body.target_type,
                    target_config: body.target_config,
                    dispatch_mode: body.dispatch_mode,
                    max_attempts: body.max_attempts,
                    retry_backoff_seconds: body.retry_backoff_seconds,
                    enabled: body.enabled,
                };
                let job = metap_cron::create_job(&state.pool, tenant_id, input, created_by)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object(serde_json::to_value(&job).unwrap_or(Value::Null))))
            })
        })
        .argument(InputValue::new("input", TypeRef::named_nn("CronJobInput"))),
    );

    mutation = mutation.field(
        Field::new("updateCronJob", TypeRef::named_nn("CronJob"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                let raw = json_arg(&ctx, "input")?;
                let body: UpdateCronJobInput = serde_json::from_value(raw).map_err(|e| validation_err(e.to_string()))?;
                if let Some(target_type) = &body.target_type {
                    if metap_cron::TargetType::parse(target_type).is_none() {
                        return Err(validation_err(
                            "`targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, steps.",
                        ));
                    }
                }
                if let Some(dispatch_mode) = &body.dispatch_mode {
                    if metap_cron::DispatchMode::parse(dispatch_mode).is_none() {
                        return Err(validation_err("`dispatchMode` must be one of: outbox, direct."));
                    }
                }
                if let Some(max_attempts) = body.max_attempts {
                    if max_attempts < 1 {
                        return Err(validation_err("`maxAttempts` must be at least 1."));
                    }
                }
                if let Some(retry_backoff_seconds) = body.retry_backoff_seconds {
                    if retry_backoff_seconds < 0 {
                        return Err(validation_err("`retryBackoffSeconds` must be non-negative."));
                    }
                }
                if body.trigger_type.is_some()
                    || body.trigger_config.is_some()
                    || body.cron_expr.is_some()
                    || body.timezone.is_some()
                {
                    let existing = metap_cron::get_job(&state.pool, tenant_id, id)
                        .await
                        .map_err(anyhow_err)?
                        .ok_or_else(not_found_cron_job)?;
                    let trigger_type = body.trigger_type.as_deref().unwrap_or(&existing.trigger_type);
                    let trigger_config = body.trigger_config.as_ref().or(existing.trigger_config.as_ref());
                    let cron_expr = body.cron_expr.as_deref().or(existing.cron_expr.as_deref());
                    let timezone = body.timezone.as_deref().unwrap_or(&existing.timezone);
                    validate_trigger(trigger_type, trigger_config, cron_expr, timezone)?;
                }
                if body.target_type.is_some() || body.target_config.is_some() {
                    let existing = metap_cron::get_job(&state.pool, tenant_id, id)
                        .await
                        .map_err(anyhow_err)?
                        .ok_or_else(not_found_cron_job)?;
                    let target_type = body.target_type.as_deref().unwrap_or(&existing.target_type);
                    let target_config = body.target_config.as_ref().unwrap_or(&existing.target_config);
                    validate_target_config(target_type, target_config)?;
                }
                let update = metap_cron::JobUpdate {
                    name: body.name,
                    trigger_type: body.trigger_type,
                    trigger_config: body.trigger_config,
                    cron_expr: body.cron_expr,
                    timezone: body.timezone,
                    target_type: body.target_type,
                    target_config: body.target_config,
                    dispatch_mode: body.dispatch_mode,
                    max_attempts: body.max_attempts,
                    retry_backoff_seconds: body.retry_backoff_seconds,
                    enabled: body.enabled,
                };
                let job = metap_cron::update_job(&state.pool, tenant_id, id, update)
                    .await
                    .map_err(anyhow_err)?
                    .ok_or_else(not_found_cron_job)?;
                Ok(Some(json_object(serde_json::to_value(&job).unwrap_or(Value::Null))))
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("input", TypeRef::named_nn("CronJobUpdateInput"))),
    );

    mutation = mutation.field(
        Field::new("deleteCronJob", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                metap_cron::delete_job(&state.pool, tenant_id, id)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(true_field()))
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID))),
    );

    // --- Mutation: oauth admin clients -----------------------------------------------------------
    mutation = mutation.field(
        Field::new("createOAuthClient", TypeRef::named_nn("OAuthClient"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let name = ctx.args.try_get("name")?.string()?.to_string();
                let redirect_uris = string_list_arg_opt(&ctx, "redirectUris")?.unwrap_or_default();
                let allowed_scopes = string_list_arg_opt(&ctx, "allowedScopes")?.unwrap_or_default();
                let is_confidential = ctx
                    .args
                    .get("isConfidential")
                    .and_then(|v| v.boolean().ok())
                    .unwrap_or(true);

                let external_subject = format!("oauth-client-{}", Uuid::new_v4());
                let service_user = metap_auth::jit_provision_external_user(
                    &state.pool,
                    tenant_id,
                    "oauth2_client_credentials",
                    &format!("{external_subject}@service.internal"),
                    &external_subject,
                )
                .await
                .map_err(anyhow_err)?;

                let (client, secret) = metap_oauth_server::create_client(
                    &state.pool,
                    metap_oauth_server::CreateClientInput {
                        tenant_id,
                        name,
                        redirect_uris,
                        allowed_scopes,
                        is_confidential,
                        service_user_id: service_user.id,
                    },
                )
                .await
                .map_err(anyhow_err)?;
                let mut dto = client_to_json(&client);
                dto.as_object_mut()
                    .expect("client_to_json always returns an object")
                    .insert("clientSecret".to_string(), json!(secret));
                Ok(Some(json_object(dto)))
            })
        })
        .argument(InputValue::new("name", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new(
            "redirectUris",
            TypeRef::named_nn_list_nn(TypeRef::STRING),
        ))
        .argument(InputValue::new(
            "allowedScopes",
            TypeRef::named_nn_list(TypeRef::STRING),
        ))
        .argument(InputValue::new("isConfidential", TypeRef::named(TypeRef::BOOLEAN))),
    );

    mutation = mutation.field(
        Field::new("revokeOAuthClient", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let id = uuid_arg(&ctx, "id")?;
                match metap_oauth_server::revoke_client(&state.pool, tenant_id, id)
                    .await
                    .map_err(anyhow_err)?
                {
                    true => Ok(Some(true_field())),
                    false => Err(gql_err(404, "not_found", "No such client in your tenant.")),
                }
            })
        })
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID))),
    );

    // --- Mutation: dashboards --------------------------------------------------------------------
    mutation = mutation.field(
        Field::new("setMyDashboard", TypeRef::named_nn("DashboardConfig"), |ctx| {
            FieldFuture::new(async move {
                let (state, context, tenant_id) = caller(&ctx)?;
                let user_id = user_id_of(context)?;
                let layout = json_arg(&ctx, "layout")?;
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let config = metap_dashboards::upsert_personal(&mut *tx, tenant_id, user_id, layout)
                    .await
                    .map_err(anyhow_err)?;
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                Ok(Some(json_object(dashboard_to_json(&config))))
            })
        })
        .argument(InputValue::new("layout", TypeRef::named_nn(JSON_SCALAR))),
    );

    mutation = mutation.field(
        Field::new(
            "setTenantDefaultDashboard",
            TypeRef::named_nn("DashboardConfig"),
            |ctx| {
                FieldFuture::new(async move {
                    let (state, context, tenant_id) = require_admin(&ctx)?;
                    let user_id = user_id_of(context)?;
                    let layout = json_arg(&ctx, "layout")?;
                    let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                    let config = metap_dashboards::upsert_tenant_default(&mut *tx, tenant_id, layout, user_id)
                        .await
                        .map_err(anyhow_err)?;
                    tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                    Ok(Some(json_object(dashboard_to_json(&config))))
                })
            },
        )
        .argument(InputValue::new("layout", TypeRef::named_nn(JSON_SCALAR))),
    );

    // --- Mutation: preferences -------------------------------------------------------------------
    mutation = mutation.field(
        Field::new("setMyPreferences", TypeRef::named_nn("Preferences"), |ctx| {
            FieldFuture::new(async move {
                let locale = ctx.args.try_get("locale")?.string()?.to_string();
                const SUPPORTED_LOCALES: [&str; 2] = ["en", "vi"];
                if !SUPPORTED_LOCALES.contains(&locale.as_str()) {
                    return Err(validation_err(format!(
                        "`locale` must be one of: {}.",
                        SUPPORTED_LOCALES.join(", ")
                    )));
                }
                let (state, context, tenant_id) = caller(&ctx)?;
                let user_id = user_id_of(context)?;
                metap_peripherals::set_locale(&state.pool, tenant_id, user_id, &locale)
                    .await
                    .map_err(anyhow_err)?;
                Ok(Some(json_object(json!({ "locale": locale }))))
            })
        })
        .argument(InputValue::new("locale", TypeRef::named_nn(TypeRef::STRING))),
    );

    // --- Mutation: platform/tenant config ----------------------------------------------------
    mutation = mutation.field(
        Field::new("setPlatformConfig", TypeRef::named_nn("SetConfigResult"), |ctx| {
            FieldFuture::new(async move {
                let state = require_platform_admin(&ctx)?;
                let key = ctx.args.try_get("key")?.string()?.to_string();
                let value = json_arg(&ctx, "value")?;
                state
                    .config
                    .set_platform_global(&key, value.clone())
                    .await
                    .map_err(config_err)?;
                Ok(Some(json_object(json!({
                    "key": key,
                    "value": value,
                    "appliesImmediately": applies_immediately(&key),
                }))))
            })
        })
        .argument(InputValue::new("key", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("value", TypeRef::named_nn(JSON_SCALAR))),
    );

    mutation = mutation.field(
        Field::new("resetPlatformConfig", TypeRef::named_nn("SetConfigResult"), |ctx| {
            FieldFuture::new(async move {
                let state = require_platform_admin(&ctx)?;
                let key = ctx.args.try_get("key")?.string()?.to_string();
                state.config.reset_platform_global(&key).await.map_err(config_err)?;
                let value = state.config.current().get(&key);
                Ok(Some(json_object(json!({
                    "key": key,
                    "value": value,
                    "appliesImmediately": applies_immediately(&key),
                }))))
            })
        })
        .argument(InputValue::new("key", TypeRef::named_nn(TypeRef::STRING))),
    );

    mutation = mutation.field(
        Field::new("setTenantConfig", TypeRef::named_nn("SetTenantConfigResult"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let key = ctx.args.try_get("key")?.string()?.to_string();
                let value = json_arg(&ctx, "value")?;
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                let is_secret = metap_config::keys::lookup(&key).is_some_and(|d| d.secret);
                let stored_value = if is_secret {
                    // A credential key's plaintext never goes into `tenant_configs` —
                    // `ConfigStore::set_tenant` refuses it outright (`NotWritable`, "holds a
                    // credential"). The intended orchestration is `metap-config`'s own doc
                    // comment on `validate_tenant_secret`: validate the plaintext here (this
                    // layer is the one place that has both `ConfigStore` and `SecretStore`),
                    // write it to the tenant's `SecretStore` entry, then persist only the
                    // server-derived `{"secretRef": ...}` marker. Found live during this
                    // migration that no REST route ever actually wired this — `set_tenant`'s
                    // refusal was unconditionally reachable and nothing called the secret path,
                    // making credential keys unsettable via HTTP at all; fixed here rather than
                    // carried forward as GraphQL's own version of the same gap.
                    let plaintext = value
                        .as_str()
                        .ok_or_else(|| validation_err("a credential value must be a string"))?;
                    state.config.validate_tenant_secret(&key, &value).map_err(config_err)?;
                    let reference = metap_control::tenant_secret_ref(tenant_id, &key);
                    state
                        .router
                        .secrets()
                        .put_secret(&reference, plaintext)
                        .await
                        .map_err(anyhow_err)?;
                    state
                        .config
                        .set_tenant_secret_marker(&mut *tx, tenant_id, &key, &reference)
                        .await
                        .map_err(config_err)?;
                    json!({ "secretRef": reference })
                } else {
                    state
                        .config
                        .set_tenant(&mut *tx, tenant_id, &key, value.clone())
                        .await
                        .map_err(config_err)?;
                    value
                };
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                Ok(Some(json_object(
                    json!({ "key": key, "value": stored_value, "overridden": true }),
                )))
            })
        })
        .argument(InputValue::new("key", TypeRef::named_nn(TypeRef::STRING)))
        .argument(InputValue::new("value", TypeRef::named_nn(JSON_SCALAR))),
    );

    mutation = mutation.field(
        Field::new("resetTenantConfig", TypeRef::named_nn("SetTenantConfigResult"), |ctx| {
            FieldFuture::new(async move {
                let (state, _context, tenant_id) = require_admin(&ctx)?;
                let key = ctx.args.try_get("key")?.string()?.to_string();
                let mut tx = state.router.begin(tenant_id.into()).await.map_err(anyhow_err)?;
                state
                    .config
                    .reset_tenant(&mut *tx, tenant_id, &key)
                    .await
                    .map_err(config_err)?;
                tx.commit().await.map_err(|e| anyhow_err(e.into()))?;
                // Clearing a credential key must revoke it from the backend, not just unlink
                // the marker row — the cron executor derives the same reference itself at
                // send time and never reads this row, so a row-only delete would leave a live
                // credential still being sent (same reasoning `metap-config`'s own doc
                // comments give).
                if metap_config::keys::lookup(&key).is_some_and(|d| d.secret) {
                    let reference = metap_control::tenant_secret_ref(tenant_id, &key);
                    state
                        .router
                        .secrets()
                        .delete_secret(&reference)
                        .await
                        .map_err(anyhow_err)?;
                }
                let value = state.effective_config(tenant_id).await.get(&key);
                Ok(Some(json_object(
                    json!({ "key": key, "value": value, "overridden": false }),
                )))
            })
        })
        .argument(InputValue::new("key", TypeRef::named_nn(TypeRef::STRING))),
    );

    (builder, query, mutation)
}

fn level_name(level: metap_config::ConfigLevel) -> &'static str {
    match level {
        metap_config::ConfigLevel::Operator => "operator",
        metap_config::ConfigLevel::PlatformGlobal => "platformGlobal",
        metap_config::ConfigLevel::Tenant => "tenant",
    }
}

/// Mirrors `routes/platform_config.rs`'s `applies_immediately` — only the rate-limit keys need a
/// restart, everything else is read per-use.
fn applies_immediately(key: &str) -> bool {
    !matches!(
        key,
        metap_config::keys::HTTP_RATE_LIMIT_PER_MS | metap_config::keys::HTTP_RATE_LIMIT_BURST
    )
}
