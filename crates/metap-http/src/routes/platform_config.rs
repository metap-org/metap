//! `GET`/`PUT`/`DELETE /platform/config` — the platform-admin surface over `metap_config`'s
//! platform-writable tiers (`docs/features/18-config-tiers-db-backed.md`, slices 1-2).
//!
//! Gated by `PlatformAdminContext`, not `AdminContext`: these keys are fleet-wide, so a tenant's
//! own admin has no business reading or writing them. Since slice 2 this surface also sets the
//! **fleet default** for `Tenant`-tier keys — the middle link of
//! `declared default <- platform_configs <- tenant_configs` — which each tenant may then override
//! through `/admin/config` (`crate::routes::tenant_config`). A key's `level` comes back with every
//! entry so a platform admin can tell the two apart.
//!
//! Both are still a *weaker* gate than the one protecting the `Operator` tier, which no route here
//! can reach at all — see `metap_config::keys`'s doc comment for why the SSRF/CORS settings from
//! audit 04 A#1/A#4 are deliberately unreachable from every API including this one.
//!
//! Deliberately not an `EntityDefinition`: same category as `policies`/`cron_jobs`/
//! `dashboard_configs` — platform plumbing, not a tenant's business data.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metap_config::ConfigError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::auth::PlatformAdminContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

/// `Operator` keys are absent from this listing entirely, not rendered as forbidden — an API that
/// cannot write them has no reason to disclose their values either.
async fn list_config(State(state): State<AppState>, PlatformAdminContext(_): PlatformAdminContext) -> Response {
    let snapshot = state.config.current();
    let items: Vec<Value> = snapshot
        .platform_writable_view()
        .into_iter()
        .map(|(def, value)| {
            json!({
                "key": def.key,
                "value": value,
                "level": level_name(def.level),
                // A platform admin setting one of these is setting a default, not a decision — the
                // value a tenant sees may be its own.
                "tenantOverridable": def.level == metap_config::ConfigLevel::Tenant,
            })
        })
        .collect();
    Json(json!({ "data": items })).into_response()
}

pub(crate) fn level_name(level: metap_config::ConfigLevel) -> &'static str {
    match level {
        metap_config::ConfigLevel::Operator => "operator",
        metap_config::ConfigLevel::PlatformGlobal => "platformGlobal",
        metap_config::ConfigLevel::Tenant => "tenant",
    }
}

#[derive(Deserialize)]
struct SetConfigBody {
    value: Value,
}

pub(crate) fn config_error_response(err: ConfigError) -> Response {
    match err {
        // 404, not 400: an unknown key is an addressing mistake, and this route is keyed by path.
        ConfigError::UnknownKey(key) => service_error_response(
            404,
            "unknown_config_key",
            Some(&format!("No config key named {key:?}.")),
            None,
        ),
        // 403 rather than 400 — the value may well have been fine; the caller is not allowed to
        // set this key at all, and saying so plainly is what makes the tier boundary legible.
        ConfigError::NotWritable { key, reason } => service_error_response(
            403,
            "config_key_not_writable",
            Some(&format!("Config key {key:?} cannot be set here: {reason}")),
            None,
        ),
        ConfigError::Invalid { key, reason } => service_error_response(
            422,
            "invalid_config_value",
            Some(&format!("Invalid value for {key:?}: {reason}")),
            None,
        ),
        ConfigError::Db(e) => internal_error_response(e.into()),
    }
}

/// The response reports which of the two propagation shapes this key has, because they genuinely
/// differ and a caller that assumes the wrong one is left debugging a change that "didn't work":
/// most keys are read per use and apply immediately, but the rate limit is baked into a middleware
/// layer when `build_router` runs and therefore needs a restart.
fn applies_immediately(key: &str) -> bool {
    !matches!(
        key,
        metap_config::keys::HTTP_RATE_LIMIT_PER_MS | metap_config::keys::HTTP_RATE_LIMIT_BURST
    )
}

async fn set_config(
    State(state): State<AppState>,
    PlatformAdminContext(_): PlatformAdminContext,
    Path(key): Path<String>,
    Json(body): Json<SetConfigBody>,
) -> Response {
    match state.config.set_platform_global(&key, body.value.clone()).await {
        Ok(()) => Json(json!({
            "data": {
                "key": key,
                "value": body.value,
                "appliesImmediately": applies_immediately(&key),
            }
        }))
        .into_response(),
        Err(e) => config_error_response(e),
    }
}

/// Clears an override so the key falls back to its declared default — distinct from setting it to
/// the default's current value, which would pin it against a future change to that default.
async fn reset_config(
    State(state): State<AppState>,
    PlatformAdminContext(_): PlatformAdminContext,
    Path(key): Path<String>,
) -> Response {
    match state.config.reset_platform_global(&key).await {
        Ok(()) => {
            let value = state.config.current().get(&key);
            Json(json!({
                "data": { "key": key, "value": value, "appliesImmediately": applies_immediately(&key) }
            }))
            .into_response()
        }
        Err(e) => config_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new().route("/platform/config", get(list_config)).route(
        "/platform/config/{key}",
        axum::routing::put(set_config).delete(reset_config),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The rate limit is the one key whose change needs a restart. If someone later moves it onto a
    /// per-request read, this test is the reminder to update the response too.
    #[test]
    fn only_the_rate_limit_keys_report_needing_a_restart() {
        assert!(!applies_immediately(metap_config::keys::HTTP_RATE_LIMIT_PER_MS));
        assert!(!applies_immediately(metap_config::keys::HTTP_RATE_LIMIT_BURST));
        assert!(applies_immediately(metap_config::keys::AUTH_SESSION_TTL_SECONDS));
        assert!(applies_immediately(metap_config::keys::GRAPHQL_MAX_DEPTH));
    }
}
