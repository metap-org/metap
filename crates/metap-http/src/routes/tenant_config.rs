//! The two non-platform config surfaces (`docs/features/18-config-tiers-db-backed.md`, slice 2):
//!
//! - `GET`/`PUT`/`DELETE /admin/config` — a tenant admin reading and setting its own `Tenant`-tier
//!   overrides. The tenant comes from the caller's verified token and from nowhere else, which is
//!   the whole of the cross-tenant isolation story here: there is no request shape that names a
//!   tenant, so there is none to get wrong.
//! - `GET /public/config` — **unauthenticated**, resolving the tenant from the `Host` header, and
//!   returning only keys declared `public` in `metap_config::keys::REGISTRY`.
//!
//! **Why the public one has to exist separately.** Branding must render on the login screen, and at
//! that moment the browser has no session and the server knows nothing about who is asking. So the
//! theme cannot be served by `/admin/config`, and the resolution cannot come from a JWT. That is a
//! genuinely different trust context, and it is handled by narrowing what the endpoint can say
//! rather than by trusting the request more:
//!
//! - The key allowlist is `ConfigKeyDef::public`, declared in Rust. A key added to the registry is
//!   private unless its own declaration opts in, so no future key leaks out here by default.
//! - An unrecognized hostname returns the **fleet-wide values**, not a 404. Answering differently
//!   for a registered and an unregistered hostname would make this a tenant-existence oracle for
//!   anyone who can send a `Host` header.
//! - The values themselves are validated far more strictly than an admin-only key would need,
//!   because they are rendered into a page: see `metap_config::keys`'s `hex_color`/`logo_url`.
//!
//! The `Host` header is attacker-controlled and is treated purely as a presentation hint. Nothing
//! here makes an authorization decision from it, and nothing else in the platform reads it.

use axum::extract::{Path, State};
use axum::http::HeaderMap;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::auth::{AdminContext, AuthContext};
use crate::error::{internal_error_response, router_unavailable_response};
use crate::routes::platform_config::level_name;
use crate::state::AppState;

/// Read is `AuthContext`, not `AdminContext`: every one of these keys shapes what the app looks
/// like or how long a session lasts for the user reading it, so an ordinary member seeing the
/// effective values is expected. Writing is admin-gated below.
async fn list_tenant_config(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let effective = state.effective_config(tenant_id).await;
    let items: Vec<Value> = effective
        .tenant_view()
        .into_iter()
        .map(|(def, value, overridden)| {
            json!({
                "key": def.key,
                "value": value,
                "level": level_name(def.level),
                // Distinguishes "we chose this" from "the platform chose this for us" — without it a
                // tenant admin cannot tell whether clearing the key would change anything.
                "overridden": overridden,
                "public": def.public,
            })
        })
        .collect();
    Json(json!({ "data": items })).into_response()
}

#[derive(Deserialize)]
struct SetConfigBody {
    value: Value,
}

async fn set_tenant_config(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Path(key): Path<String>,
    Json(body): Json<SetConfigBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    match state
        .config
        .set_tenant(&mut *tx, tenant_id, &key, body.value.clone())
        .await
    {
        Ok(()) => {
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            Json(json!({ "data": { "key": key, "value": body.value, "overridden": true } })).into_response()
        }
        Err(e) => crate::routes::platform_config::config_error_response(e),
    }
}

/// Clears this tenant's override. The key then reads back the fleet default a platform admin set,
/// or the value declared in Rust if there is none — never `null`.
async fn reset_tenant_config(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Path(key): Path<String>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    match state.config.reset_tenant(&mut *tx, tenant_id, &key).await {
        Ok(()) => {
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            let value = state.effective_config(tenant_id).await.get(&key);
            Json(json!({ "data": { "key": key, "value": value, "overridden": false } })).into_response()
        }
        Err(e) => crate::routes::platform_config::config_error_response(e),
    }
}

/// The `Host` header, or `None` when it is absent or unusable.
///
/// `X-Forwarded-Host` is deliberately **not** consulted. It is a header any client can set, and
/// honoring it would let a caller pick which tenant's branding to be served regardless of which
/// hostname they actually reached — turning the one thing this endpoint keys on into a free
/// parameter. A deployment behind a proxy should preserve `Host`, which is the standard
/// configuration for every reverse proxy this platform would sit behind.
fn request_hostname(headers: &HeaderMap) -> Option<String> {
    let raw = headers.get(axum::http::header::HOST)?.to_str().ok()?;
    metap_control::normalize_hostname(raw)
}

async fn public_config(State(state): State<AppState>, headers: HeaderMap) -> Response {
    let tenant_id = match request_hostname(&headers) {
        Some(host) => resolve_hostname(&state, &host).await,
        None => None,
    };

    // No tenant (unknown hostname, or no usable Host header) is not an error — it reads back the
    // fleet-wide values, which is also what a tenant that has set nothing would see.
    let effective = match tenant_id {
        Some(id) => state.effective_config(id).await,
        None => state.config.effective(None),
    };

    let items: Vec<Value> = effective
        .public_view()
        .into_iter()
        .map(|(key, value)| json!({ "key": key, "value": value }))
        .collect();
    Json(json!({ "data": items })).into_response()
}

async fn resolve_hostname(state: &AppState, host: &str) -> Option<Uuid> {
    let pool = state.pool.clone();
    let host_owned = host.to_string();
    match state
        .tenant_hostname_cache
        .get_with(host, move || async move {
            metap_control::tenant_id_for_hostname(&pool, &host_owned)
                .await
                .map_err(anyhow::Error::from)
        })
        .await
    {
        Ok(id) => id,
        Err(e) => {
            // Degrade to fleet defaults rather than failing: a login page that will not render
            // because a branding lookup failed is a far worse outcome than an unbranded one.
            tracing::warn!(host, error = %e, "tenant hostname lookup failed; serving fleet-wide public config");
            None
        }
    }
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/admin/config", get(list_tenant_config))
        .route(
            "/admin/config/{key}",
            axum::routing::put(set_tenant_config).delete(reset_tenant_config),
        )
        .route("/public/config", get(public_config))
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderValue;

    fn headers_with_host(value: &str) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(axum::http::header::HOST, HeaderValue::from_str(value).unwrap());
        headers
    }

    #[test]
    fn the_hostname_comes_from_host_and_never_from_a_forwarded_header() {
        let mut headers = headers_with_host("real.example.com");
        headers.insert("x-forwarded-host", HeaderValue::from_static("victim.example.com"));
        assert_eq!(request_hostname(&headers).as_deref(), Some("real.example.com"));
    }

    #[test]
    fn a_missing_or_malformed_host_resolves_to_no_tenant() {
        assert_eq!(request_hostname(&HeaderMap::new()), None);
        assert_eq!(request_hostname(&headers_with_host("not a hostname")), None);
    }

    /// The public surface is defined by the registry's own `public` flag, so this asserts the shape
    /// of that allowlist rather than the handler: exactly the branding keys, and nothing that a
    /// tenant admin set for internal use.
    #[test]
    fn only_branding_keys_are_public() {
        let public: Vec<&str> = metap_config::keys::REGISTRY
            .iter()
            .filter(|d| d.public)
            .map(|d| d.key)
            .collect();
        assert_eq!(
            public,
            vec![
                metap_config::keys::THEME_PRIMARY_COLOR,
                metap_config::keys::THEME_LOGO_URL,
                metap_config::keys::THEME_DISPLAY_NAME,
            ]
        );
        // The one tenant-tier key that is *not* branding must stay off this list — it is the reason
        // the public view filters by flag instead of by tier.
        assert!(!public.contains(&metap_config::keys::AUTH_SESSION_TTL_SECONDS));
    }
}
