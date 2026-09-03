//! Env-var-numbered upstream configuration — `UPSTREAM_1_NAME`, `UPSTREAM_1_GRPC_ADDR`,
//! `UPSTREAM_1_METADATA_URL`, `UPSTREAM_1_LOGIN_URL`, `UPSTREAM_1_SERVICE_EMAIL`,
//! `UPSTREAM_1_SERVICE_PASSWORD`, `UPSTREAM_2_...`, stopping at the first missing `_NAME` — no
//! config-parsing dependency added, matching this platform's other env-var-heavy ops binaries
//! (`cron-scheduler`, `outbox-publisher`).

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
    /// per-upstream service credentials above (see `crate::server`'s doc comment).
    pub auth_public_key_pem: Vec<u8>,
    pub cors_origins: Vec<String>,
    pub is_production: bool,
}

use metap_runtime::env::{env_or, require_env};

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

        let key_path = require_env("AUTH_JWT_PUBLIC_KEY_PATH")
            .map_err(|e| anyhow::anyhow!("{e} — this gateway's own keypair (see .env.example)"))?;
        let auth_public_key_pem =
            std::fs::read(&key_path).map_err(|e| anyhow::anyhow!("failed to read {key_path}: {e}"))?;

        let mut upstreams = Vec::new();
        let mut i = 1u32;
        // `while let`, not `loop { let ... else { break } }` — clippy's `while_let_loop` (which CI's
        // newer toolchain enforces and an older local one does not) rejects the latter.
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
                "no upstreams configured — set UPSTREAM_1_NAME/UPSTREAM_1_GRPC_ADDR/\
                 UPSTREAM_1_METADATA_URL/UPSTREAM_1_LOGIN_URL/UPSTREAM_1_SERVICE_EMAIL/\
                 UPSTREAM_1_SERVICE_PASSWORD (see .env.example)"
            );
        }

        Ok(Self {
            host,
            port,
            graphql_max_depth,
            graphql_max_complexity,
            upstreams,
            auth_public_key_pem,
            cors_origins,
            is_production,
        })
    }
}
