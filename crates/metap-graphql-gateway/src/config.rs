//! Upstream configuration — two mutually exclusive sources, picked by whether
//! `UPSTREAM_CONFIG_FILE` is set (`docs/features/33-declarative-yaml-app-bootstrap.md`):
//! - **File** (`UPSTREAM_CONFIG_FILE=path/to/upstreams.yaml`): one YAML file listing every
//!   upstream under an `upstreams:` key, same field names as `UpstreamConfig` itself
//!   (`camelCase` — `grpcAddr`/`metadataUrl`/...). Meant for a deployment with more than a
//!   couple of upstreams, where N numbered env-var blocks stop being reviewable as one unit.
//! - **Env vars** (default, unchanged): `UPSTREAM_1_NAME`, `UPSTREAM_1_GRPC_ADDR`,
//!   `UPSTREAM_1_METADATA_URL`, `UPSTREAM_1_LOGIN_URL`, `UPSTREAM_1_SERVICE_EMAIL`,
//!   `UPSTREAM_1_SERVICE_PASSWORD`, `UPSTREAM_2_...`, stopping at the first missing `_NAME` —
//!   matches this platform's other env-var-heavy ops binaries (`cron-scheduler`,
//!   `outbox-publisher`). Every existing deployment (`../metap-demo-waf`'s
//!   `waf-graphql-gateway`) leaves `UPSTREAM_CONFIG_FILE` unset and is unaffected.
//!
//! Every other `GatewayConfig` field (host/port/limits/auth/CORS) stays env-var-only — this
//! binary owns no Postgres pool to read a `metap_config`-style tiered config from (see
//! `GatewayConfig::graphql_max_depth`'s own doc comment), and those fields don't suffer from the
//! same "N numbered blocks" unwieldiness the upstream list does.

use serde::Deserialize;

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UpstreamConfig {
    pub name: String,
    /// Full URI (`http://host:port`) — passed straight to `GrpcBackend::connect`.
    pub grpc_addr: String,
    /// Full URL to that upstream's `GET /metadata/entities` (e.g.
    /// `http://localhost:3100/metadata/entities`).
    pub metadata_url: String,
    /// Full URL to that upstream's `POST /auth/login` (e.g. `http://localhost:3100/auth/login`) —
    /// this gateway's own identity for calling this one upstream logs in here rather than reusing
    /// a hand-minted-once JWT; see `metap_grpc::ServiceTokenSource`'s doc comment for why this
    /// isn't per-caller and how it's kept fresh.
    pub login_url: String,
    pub service_email: String,
    pub service_password: String,
}

/// Top-level shape of `UPSTREAM_CONFIG_FILE`'s YAML — just a list, wrapped in a named key rather
/// than a bare top-level array so the file has room to grow another top-level key later without
/// becoming ambiguous.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct UpstreamConfigFile {
    upstreams: Vec<UpstreamConfig>,
}

/// Parses `UPSTREAM_CONFIG_FILE`'s contents — split out from `GatewayConfig::from_env` so it's
/// unit-testable against a string literal without touching the filesystem or env vars.
fn parse_upstreams_yaml(source: &str) -> anyhow::Result<Vec<UpstreamConfig>> {
    let file: UpstreamConfigFile =
        serde_norway::from_str(source).map_err(|e| anyhow::anyhow!("invalid UPSTREAM_CONFIG_FILE YAML: {e}"))?;
    if file.upstreams.is_empty() {
        anyhow::bail!("UPSTREAM_CONFIG_FILE was set but its \"upstreams\" list is empty");
    }
    Ok(file.upstreams)
}

pub struct GatewayConfig {
    pub host: String,
    pub port: u16,
    /// GraphQL depth/complexity guardrails (audit 04 A#7 — these were `SchemaLimits::default()`
    /// hard-coded at the call site, with no way to retune short of a rebuild).
    ///
    /// **Env vars here, not `metap_config`'s `platform_configs` table**, unlike every other
    /// consumer of these two numbers. This binary owns no Postgres pool at all (it is a pure BFF:
    /// no entity, no `CrudService`, no database), so it has nothing to read that table from. Env is
    /// the mechanism actually available to it, and it is exactly what the finding asked for
    /// ("không chỉnh qua env"). A service that *does* have a pool reads the same two values from
    /// config instead — see `metap_config::keys`.
    pub graphql_max_depth: usize,
    pub graphql_max_complexity: usize,
    pub upstreams: Vec<UpstreamConfig>,
    /// This gateway's own keypair, decode-only — gates access to `/graphql`, unrelated to any
    /// per-upstream service credentials above (see `crate::server`'s doc comment). `None` when
    /// `jwks_url` is set instead — the 2 are mutually exclusive verification trust roots, same
    /// as `metap_jwks::TokenVerifier`'s `Static`/`Jwks` variants this eventually builds.
    pub auth_public_key_pem: Option<Vec<u8>>,
    /// `GET /.well-known/jwks.json` of the trust root to verify against (`metap-jwks`) — set this
    /// instead of `AUTH_JWT_PUBLIC_KEY_PATH` when the gateway's upstreams mint EdDSA tokens via a
    /// shared `metap-jwks` key (e.g. `../metap-demo-waf`'s 3 services) rather than this crate's
    /// original static RS256 keypair. `None` (the default) preserves every existing deployment's
    /// behavior unchanged.
    pub jwks_url: Option<String>,
    /// Opt-in cookie fallback for `/graphql` (`COOKIE_AUTH_ENABLED`, default `false` — every
    /// existing deployment's `Authorization: Bearer`-only behavior is unchanged unless this is
    /// set) — see `crate::server::authenticate`'s doc comment for the same-origin assumption this
    /// relies on. Added so `../metap-demo-waf`'s frontend can call `/graphql` directly off its
    /// existing session cookie instead of minting a fresh short-lived Bearer token via `GET
    /// /auth/token` before every call (`@metap/platform-ui`'s `useGraphQLQuery`) — that extra
    /// round trip was the whole reason this exists.
    pub cookie_auth_enabled: bool,
    pub cors_origins: Vec<String>,
    pub is_production: bool,
}

use metap_runtime::env::{env_or, flag_enabled, require_env};

impl GatewayConfig {
    pub fn from_env() -> anyhow::Result<Self> {
        let host = env_or("HOST", "0.0.0.0".to_string());
        let port: u16 = env_or("PORT", 4000);
        // Defaults match `SchemaLimits::default()` exactly, so an existing deployment that sets
        // neither var behaves as it did before this became configurable.
        let graphql_max_depth: usize = env_or("GRAPHQL_MAX_DEPTH", 10);
        let graphql_max_complexity: usize = env_or("GRAPHQL_MAX_COMPLEXITY", 1000);
        let is_production = std::env::var("NODE_ENV").is_ok_and(|v| v == "production");
        let cors_origins = std::env::var("CORS_ORIGINS")
            .map(|v| {
                v.split(',')
                    .map(str::trim)
                    .filter(|s| !s.is_empty())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default();

        // `JWKS_URL` and `AUTH_JWT_PUBLIC_KEY_PATH` are mutually exclusive trust roots — exactly
        // one must be set, same "pick a verifier" choice `metap_jwks::TokenVerifier` encodes at
        // the type level (see `crate::server::serve`, the one place this becomes a `TokenVerifier`).
        let jwks_url = std::env::var("JWKS_URL").ok().filter(|v| !v.is_empty());
        let auth_public_key_pem = match &jwks_url {
            Some(_) => None,
            None => {
                let key_path = require_env("AUTH_JWT_PUBLIC_KEY_PATH").map_err(|e| {
                    anyhow::anyhow!("{e} — this gateway's own keypair (see .env.example), or set JWKS_URL instead")
                })?;
                Some(std::fs::read(&key_path).map_err(|e| anyhow::anyhow!("failed to read {key_path}: {e}"))?)
            }
        };

        let upstreams = match std::env::var("UPSTREAM_CONFIG_FILE") {
            Ok(path) => {
                let source = std::fs::read_to_string(&path)
                    .map_err(|e| anyhow::anyhow!("failed to read UPSTREAM_CONFIG_FILE {path}: {e}"))?;
                parse_upstreams_yaml(&source)?
            }
            Err(_) => {
                let mut upstreams = Vec::new();
                let mut i = 1u32;
                // `while let`, not `loop { let ... else { break } }` — clippy's `while_let_loop`
                // (which CI's newer toolchain enforces and an older local one does not) rejects
                // the latter.
                while let Ok(name) = std::env::var(format!("UPSTREAM_{i}_NAME")) {
                    let grpc_addr = require_env(&format!("UPSTREAM_{i}_GRPC_ADDR"))?;
                    let metadata_url = require_env(&format!("UPSTREAM_{i}_METADATA_URL"))?;
                    let login_url = require_env(&format!("UPSTREAM_{i}_LOGIN_URL"))?;
                    let service_email = require_env(&format!("UPSTREAM_{i}_SERVICE_EMAIL"))?;
                    let service_password = require_env(&format!("UPSTREAM_{i}_SERVICE_PASSWORD"))?;
                    upstreams.push(UpstreamConfig {
                        name,
                        grpc_addr,
                        metadata_url,
                        login_url,
                        service_email,
                        service_password,
                    });
                    i += 1;
                }
                if upstreams.is_empty() {
                    anyhow::bail!(
                        "no upstreams configured — set UPSTREAM_CONFIG_FILE, or UPSTREAM_1_NAME/\
                         UPSTREAM_1_GRPC_ADDR/UPSTREAM_1_METADATA_URL/UPSTREAM_1_LOGIN_URL/\
                         UPSTREAM_1_SERVICE_EMAIL/UPSTREAM_1_SERVICE_PASSWORD (see .env.example)"
                    );
                }
                upstreams
            }
        };

        Ok(Self {
            host,
            port,
            graphql_max_depth,
            graphql_max_complexity,
            upstreams,
            auth_public_key_pem,
            jwks_url,
            cookie_auth_enabled: flag_enabled("COOKIE_AUTH_ENABLED"),
            cors_origins,
            is_production,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_upstreams_from_yaml_with_camel_case_keys() {
        let upstreams = parse_upstreams_yaml(
            r#"
upstreams:
  - name: crm
    grpcAddr: "http://localhost:5100"
    metadataUrl: "http://localhost:3100/metadata/entities"
    loginUrl: "http://localhost:3100/auth/login"
    serviceEmail: "gateway@crm.local"
    servicePassword: "hunter2"
  - name: jira
    grpcAddr: "http://localhost:5200"
    metadataUrl: "http://localhost:3200/metadata/entities"
    loginUrl: "http://localhost:3200/auth/login"
    serviceEmail: "gateway@jira.local"
    servicePassword: "hunter2"
"#,
        )
        .unwrap();
        assert_eq!(upstreams.len(), 2);
        assert_eq!(upstreams[0].name, "crm");
        assert_eq!(upstreams[0].grpc_addr, "http://localhost:5100");
        assert_eq!(upstreams[1].name, "jira");
    }

    #[test]
    fn rejects_an_empty_upstreams_list() {
        let err = parse_upstreams_yaml("upstreams: []").unwrap_err();
        assert!(err.to_string().contains("empty"));
    }

    #[test]
    fn rejects_malformed_yaml_with_a_readable_error() {
        let err = parse_upstreams_yaml("not: [valid").unwrap_err();
        assert!(err.to_string().contains("invalid UPSTREAM_CONFIG_FILE YAML"));
    }
}
