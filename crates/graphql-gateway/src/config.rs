//! Env-var-numbered upstream configuration — `UPSTREAM_1_NAME`, `UPSTREAM_1_GRPC_ADDR`,
//! `UPSTREAM_1_METADATA_URL`, `UPSTREAM_1_SERVICE_JWT`, `UPSTREAM_2_...`, stopping at the first
//! missing `_NAME` — no config-parsing dependency added, matching this platform's other
//! env-var-heavy ops binaries (`cron-scheduler`, `outbox-publisher`).

pub struct UpstreamConfig {
    pub name: String,
    /// Full URI (`http://host:port`) — passed straight to `GrpcBackend::connect`.
    pub grpc_addr: String,
    /// Full URL to that upstream's `GET /metadata/entities` (e.g.
    /// `http://localhost:3100/metadata/entities`).
    pub metadata_url: String,
    /// This gateway's single, static, pre-minted identity for calling this one upstream — see
    /// `metap_grpc::GrpcBackend`'s doc comment for why this isn't per-caller.
    pub service_jwt: String,
}

pub struct GatewayConfig {
    pub host: String,
    pub port: u16,
    pub upstreams: Vec<UpstreamConfig>,
    /// This gateway's own keypair, decode-only — gates access to `/graphql`, unrelated to any
    /// `service_jwt` above (see `crate::server`'s doc comment).
    pub auth_public_key_pem: Vec<u8>,
    pub cors_origins: Vec<String>,
    pub is_production: bool,
}

use metap_runtime::env::{env_or, require_env};

impl GatewayConfig {
    pub fn from_env() -> anyhow::Result<Self> {
        let host = env_or("HOST", "0.0.0.0".to_string());
        let port: u16 = env_or("PORT", 4000);
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
        loop {
            let Ok(name) = std::env::var(format!("UPSTREAM_{i}_NAME")) else {
                break;
            };
            let grpc_addr = require_env(&format!("UPSTREAM_{i}_GRPC_ADDR"))?;
            let metadata_url = require_env(&format!("UPSTREAM_{i}_METADATA_URL"))?;
            let service_jwt = require_env(&format!("UPSTREAM_{i}_SERVICE_JWT"))?;
            upstreams.push(UpstreamConfig {
                name,
                grpc_addr,
                metadata_url,
                service_jwt,
            });
            i += 1;
        }
        if upstreams.is_empty() {
            anyhow::bail!(
                "no upstreams configured — set UPSTREAM_1_NAME/UPSTREAM_1_GRPC_ADDR/\
                 UPSTREAM_1_METADATA_URL/UPSTREAM_1_SERVICE_JWT (see .env.example)"
            );
        }

        Ok(Self {
            host,
            port,
            upstreams,
            auth_public_key_pem,
            cors_origins,
            is_production,
        })
    }
}
