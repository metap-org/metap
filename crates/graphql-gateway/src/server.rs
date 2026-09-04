//! The gateway's own minimal `axum` app — deliberately not `metap_http::build_router`/
//! `AppState`, which are tightly coupled to a Postgres pool + `CrudService` this binary has
//! neither of (see this crate's `main.rs` doc comment; `metap-http` is a dev-only dependency
//! here, used just by `tests/gateway_e2e_postgres.rs`'s upstream harness, not by this binary
//! itself). Reuses only the standalone-safe pieces: `metap_runtime::security_headers::security_headers`
//! (a plain middleware fn, no `AppState` dependency — moved out of `metap-http` 2026-09-02
//! specifically so this crate didn't have to pull in all of `metap-http` for just this one
//! function) and `metap_graphql_http::playground_router` (generalized to `Router<S>` for exactly
//! this reason).
//!
//! **Auth here decodes the caller's token, then forwards it verbatim to whichever upstream a
//! resolver ends up calling.** A request must carry a Bearer token (the default) — or, opt-in via
//! `COOKIE_AUTH_ENABLED` (`GatewayConfig::cookie_auth_enabled`), the same session cookie REST
//! already accepts — that decodes against this gateway's own trust root to reach `/graphql` at
//! all; `authenticate` keeps that raw token on `RequestContext::forwarded_bearer_token`, and
//! `GrpcBackend::signed_request` (`metap-grpc/src/client.rs`) prefers it over its configured
//! `ServiceTokenSource` when present. This is what lets a mutation through the gateway enforce
//! the REAL caller's own permissions at the upstream, not a shared service account's — but it
//! only works because the gateway and every upstream it talks to verify against the SAME trust
//! root (true of every `metap-demo-waf` service today; see that repo's
//! `graphql-gateway/.env.example`). No role/permission check happens in this gateway itself
//! either way — real enforcement always happens where it already did before this gateway
//! existed, inside each upstream's own `CrudService`/`PermissionService`, once the gRPC call
//! lands there.
//!
//! **The cookie path is same-origin only, by construction, not by any check this file makes.**
//! `/graphql` is always a `POST` regardless of whether the GraphQL operation itself is a query or
//! a mutation, so a cookie-authenticated request here is always CSRF-gated (unlike REST, which
//! exempts safe methods) — a caller must present the CSRF header (`X-CSRF-Token`) echoing the
//! double-submit cookie, exactly like `metap-http::cookies` requires for the mutating case. Enable
//! `COOKIE_AUTH_ENABLED` only for a deployment where this gateway is reached through the same
//! reverse proxy/origin as the REST services that issue the cookie (`../metap-demo-waf`'s Vite
//! dev proxy / nginx both route `/graphql` and `/auth/*` to the same origin) — a deployment
//! that fronts this gateway on its own separate origin should leave it off and keep using Bearer.

use std::sync::Arc;

use async_graphql::{BatchRequest, Executor};
use async_graphql_axum::{GraphQLBatchRequest, GraphQLResponse};
use axum::extract::State;
use axum::http::{header, HeaderMap, Method};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::Router;
use axum_extra::extract::cookie::CookieJar;
use jsonwebtoken::DecodingKey;
use metap_crud::RecordBackend;
use metap_graphql::{with_request_data, Schema};
use metap_jwks::{decode_with_verifier, TokenVerifier};
use metap_permission::RequestContext;
use metap_runtime::cookie_auth::{csrf_matches, CSRF_COOKIE_NAME, CSRF_HEADER_NAME, SESSION_COOKIE_NAME};

use crate::config::GatewayConfig;
use crate::schema_builder::BuiltSchema;

#[derive(Clone)]
struct GatewayState {
    schema: Arc<Schema>,
    backend: Arc<dyn RecordBackend>,
    verifier: Arc<TokenVerifier>,
    /// See `GatewayConfig::cookie_auth_enabled`'s doc comment. `false` for every deployment that
    /// hasn't opted in — `authenticate` then behaves exactly as it always did (Bearer-only).
    cookie_auth_enabled: bool,
}

/// Same `{"error":{"code":...,"message":...}}` shape every other axum surface in this project
/// uses (`metap_runtime::http_error::service_error_response`) — this gateway used to hand-roll a
/// plain-text 401 body instead, the one real inconsistency found reviewing `metap-http/src/error.rs`
/// for reuse (2026-08-31). Uses `metap-runtime` directly rather than `metap_http::error`'s
/// re-export of the same function — this crate happens to depend on `metap-http` too today, but
/// the point of moving these 2 functions to `metap-runtime` was so a binary that *doesn't* (a
/// from-scratch custom router, e.g. a future `../metap-demo-waf` admin API) gets the same shape
/// without the heavier dependency.
fn unauthorized(message: &str) -> Box<Response> {
    Box::new(metap_runtime::http_error::service_error_response(
        401,
        "unauthorized",
        Some(message),
        None,
    ))
}

/// Decodes the caller's Bearer token against whichever trust root this gateway is configured for
/// (`GatewayConfig`'s doc comment — static per-app keypair by default, or JWKS when `JWKS_URL` is
/// set) — no role/permission check here (see this module's doc comment for why: real enforcement
/// happens downstream, once a call reaches its owning upstream). The `Err` is boxed (clippy's
/// `result_large_err`) since a full `Response` is much larger than the `Ok` variant.
async fn authenticate(
    headers: &HeaderMap,
    verifier: &TokenVerifier,
    cookie_auth_enabled: bool,
) -> Result<RequestContext, Box<Response>> {
    let raw = headers.get(header::AUTHORIZATION).and_then(|v| v.to_str().ok());
    // An explicit `Authorization` header always wins when present, mirroring
    // `metap-http::auth::AuthContext`'s own precedence — a header can't be attached by a browser
    // to a cross-site request the way a cookie is, so nothing here needs a CSRF check.
    let token = match raw {
        Some(raw) => metap_runtime::bearer::parse_bearer(raw)
            .ok_or_else(|| unauthorized("authorization header must be a Bearer token"))?
            .to_string(),
        None if cookie_auth_enabled => {
            let jar = CookieJar::from_headers(headers);
            let session_token = jar
                .get(SESSION_COOKIE_NAME)
                .map(|c| c.value().to_string())
                .ok_or_else(|| unauthorized("missing authorization header"))?;
            // `/graphql` is always POST (query or mutation alike) — always CSRF-gated when
            // cookie-authenticated, unlike REST's safe-method exemption (this module's own doc
            // comment).
            let csrf_cookie = jar.get(CSRF_COOKIE_NAME).map(|c| c.value());
            let csrf_header = headers.get(CSRF_HEADER_NAME).and_then(|v| v.to_str().ok());
            if !csrf_matches(csrf_cookie, csrf_header) {
                return Err(unauthorized("missing or invalid csrf token"));
            }
            session_token
        }
        None => return Err(unauthorized("missing authorization header")),
    };
    let token = token.as_str();
    let claims = decode_with_verifier(token, verifier, None)
        .await
        .map_err(|_| unauthorized("invalid or expired token"))?;
    Ok(RequestContext {
        tenant_id: claims.tenant_id,
        user_id: Some(claims.sub),
        roles: None,
        function_id: claims.function_id,
        context_attributes: None,
        forwarded_bearer_token: Some(token.to_string()),
    })
}

/// Accepts either a single `{query, variables}` object or a JSON array of them in one POST body
/// (`GraphQLBatchRequest` parses both — see `async-graphql`'s `BatchRequest`), so every existing
/// caller sending a plain single-object body keeps working unchanged; a client that wants to
/// collapse several queries issued in the same tick into one round trip (e.g.
/// `platform-ui`'s `graphqlClient.ts`) can send an array instead and get an array of `{data,
/// errors}` back in the same order (`Schema::execute_batch` runs the batch concurrently via
/// `FuturesOrdered`, but the response order always matches the request order). `with_request_data`
/// builds a fresh `DataLoader` per request as before — applied to every item in a batch
/// individually, not shared across them, since it's cheap and keeps this identical to N separate
/// requests from the resolvers' point of view.
async fn graphql_handler(State(state): State<GatewayState>, headers: HeaderMap, req: GraphQLBatchRequest) -> Response {
    let context = match authenticate(&headers, &state.verifier, state.cookie_auth_enabled).await {
        Ok(context) => context,
        Err(response) => return *response,
    };
    let batch_request = match req.into_inner() {
        BatchRequest::Single(request) => {
            BatchRequest::Single(with_request_data(request, state.backend.clone(), context))
        }
        BatchRequest::Batch(requests) => BatchRequest::Batch(
            requests
                .into_iter()
                .map(|request| with_request_data(request, state.backend.clone(), context.clone()))
                .collect(),
        ),
    };
    GraphQLResponse::from(state.schema.execute_batch(batch_request).await).into_response()
}

async fn health() -> &'static str {
    "ok"
}

pub async fn serve(config: GatewayConfig, built: BuiltSchema) -> anyhow::Result<()> {
    // Exactly one of the 2 is `Some` — `GatewayConfig::from_env`'s own doc comment on
    // `jwks_url`/`auth_public_key_pem` enforces this at parse time.
    let verifier = match (&config.jwks_url, &config.auth_public_key_pem) {
        (Some(jwks_url), _) => TokenVerifier::Jwks {
            // 5 minutes — same order of magnitude as this platform's other JWKS/registry caches
            // (`RegistryCache`/`metap_jwks::JwksClient`'s own callers elsewhere all pick a TTL
            // in the tens-of-seconds-to-minutes range); not yet env-configurable, since this
            // gateway only reads it once at boot rather than per-request.
            client: Arc::new(metap_jwks::JwksClient::new(jwks_url.clone(), std::time::Duration::from_secs(300))),
            leeway: 20,
        },
        (None, Some(pem)) => TokenVerifier::Static {
            decoding_key: DecodingKey::from_rsa_pem(pem)?,
            leeway: 20,
        },
        (None, None) => anyhow::bail!("neither JWKS_URL nor AUTH_JWT_PUBLIC_KEY_PATH configured"),
    };
    let state = GatewayState {
        schema: built.schema,
        backend: built.backend,
        verifier: Arc::new(verifier),
        cookie_auth_enabled: config.cookie_auth_enabled,
    };

    let cors = metap_runtime::cors::build(
        &config.cors_origins,
        &[Method::GET, Method::POST],
        &[
            header::AUTHORIZATION,
            header::CONTENT_TYPE,
            header::ACCEPT,
            CSRF_HEADER_NAME.parse()?,
        ],
    );

    let mut app: Router<GatewayState> = Router::new()
        .route("/health", get(health))
        .route("/graphql", post(graphql_handler));

    // Same "unauthenticated static HTML, gate by env instead of by the crate itself" convention
    // `../metap-demo-jira/src/main.rs` already established for this exact router.
    if !config.is_production {
        app = app.merge(metap_graphql_http::playground_router::<GatewayState>("/graphql"));
    }

    // Rate-limit/tracing-span/request-id — this gateway had none of these at all before
    // (2026-08-31, `docs/features/08-metap-runtime-common-crate.md`'s 4th pass), unlike
    // `metap-http::build_router`'s own copy of the same defaults. Same layer order
    // `metap-http` uses: `request_id` outermost, then `trace`, then `security_headers`, then
    // `request_context`, then `rate_limit`, then `cors` innermost.
    let rate_limit = metap_runtime::rate_limit::build(200, 300);
    let trace = metap_runtime::trace::build();

    let app = app
        .layer(cors)
        .layer(rate_limit)
        .layer(axum::middleware::from_fn(
            metap_runtime::request_context::request_context,
        ))
        .layer(axum::middleware::from_fn(
            metap_runtime::security_headers::security_headers,
        ))
        .layer(trace)
        .layer(axum::middleware::from_fn(
            metap_runtime::request_id::generate_request_ids,
        ))
        .with_state(state);

    let addr = format!("{}:{}", config.host, config.port);

    // `rate_limit`'s `PeerIpKeyExtractor` reads the connection's peer address from the
    // `ConnectInfo<SocketAddr>` extension — see `metap_runtime::rate_limit::build`'s doc
    // comment — so this must serve via `into_make_service_with_connect_info`, not plain
    // `into_make_service()`.
    metap_runtime::serve::run(&addr, app.into_make_service_with_connect_info::<std::net::SocketAddr>()).await?;
    Ok(())
}
