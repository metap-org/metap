//! Mirrors packages/core/src/server/config.ts's `AppConfig`/`loadConfig` exactly: same env
//! var names, same defaults, same required-vs-optional fields. `.env` resolution matches
//! Node's `import "dotenv/config"` (current working directory) via `dotenvy::dotenv()`.

use std::env;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum NodeEnv {
    Development,
    Test,
    Production,
}

#[derive(Debug, Clone)]
pub struct AppConfig {
    pub node_env: NodeEnv,
    pub host: String,
    pub port: u16,
    pub database_url: String,
    pub outbox_database_url: Option<String>,
    pub rabbitmq_url: String,
    pub cors_origins: Vec<String>,
    pub auth_jwt_public_key_path: String,
    /// Needed only by binaries that mint tokens (`crm-server`'s `POST /auth/login` —
    /// `dev-tools mint-token` reads its own copy of the key file directly, not this config).
    /// Required, like `auth_jwt_public_key_path`: `crm-server` is no longer verify-only once
    /// it can issue tokens from a real login, so both keys are load-bearing at boot.
    pub auth_jwt_private_key_path: String,
    /// Path (resolved relative to the binary's cwd, same convention as
    /// `auth_jwt_public_key_path`) to a built frontend (`apps/crm-fe`'s `vite build` output)
    /// to serve as static files alongside the API, single-process/single-port. Unset in the
    /// normal split dev workflow (`pnpm dev:web` proxies to the API separately); set by the
    /// `pnpm start` monolith script.
    pub static_dir: Option<String>,
}

impl AppConfig {
    /// The URL a worker's outbox/event-publishing DB connection should use — the
    /// `OUTBOX_DATABASE_URL` override if set, otherwise `DATABASE_URL` — lets an outbox
    /// worker use a separate DB connection/credentials per service if ever needed, without
    /// requiring one.
    pub fn outbox_database_url(&self) -> &str {
        self.outbox_database_url.as_deref().unwrap_or(&self.database_url)
    }
}

fn is_url_like(s: &str) -> bool {
    s.contains("://") && !s.trim().is_empty()
}

pub fn load_config() -> anyhow::Result<AppConfig> {
    dotenvy::dotenv().ok();

    let node_env = match env::var("NODE_ENV").as_deref() {
        Ok("test") => NodeEnv::Test,
        Ok("production") => NodeEnv::Production,
        _ => NodeEnv::Development,
    };

    let host = env::var("HOST").unwrap_or_else(|_| "0.0.0.0".to_string());

    let port: u16 = env::var("PORT")
        .ok()
        .and_then(|v| v.parse().ok())
        .filter(|&p: &u16| p > 0)
        .unwrap_or(3000);

    let database_url = env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    if !is_url_like(&database_url) {
        anyhow::bail!("DATABASE_URL must be a valid URL");
    }

    let outbox_database_url = match env::var("OUTBOX_DATABASE_URL") {
        Ok(v) if !v.is_empty() => {
            if !is_url_like(&v) {
                anyhow::bail!("OUTBOX_DATABASE_URL must be a valid URL");
            }
            Some(v)
        }
        _ => None,
    };

    let rabbitmq_url = env::var("RABBITMQ_URL").map_err(|_| anyhow::anyhow!("RABBITMQ_URL is required"))?;
    if !is_url_like(&rabbitmq_url) {
        anyhow::bail!("RABBITMQ_URL must be a valid URL");
    }

    let cors_origins = env::var("CORS_ORIGINS")
        .ok()
        .map(|v| v.split(',').filter(|s| !s.is_empty()).map(String::from).collect())
        .unwrap_or_default();

    let auth_jwt_public_key_path = env::var("AUTH_JWT_PUBLIC_KEY_PATH")
        .ok()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| anyhow::anyhow!("AUTH_JWT_PUBLIC_KEY_PATH is required"))?;

    let auth_jwt_private_key_path = env::var("AUTH_JWT_PRIVATE_KEY_PATH")
        .ok()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| anyhow::anyhow!("AUTH_JWT_PRIVATE_KEY_PATH is required"))?;

    let static_dir = env::var("STATIC_DIR").ok().filter(|s| !s.is_empty());

    Ok(AppConfig {
        node_env,
        host,
        port,
        database_url,
        outbox_database_url,
        rabbitmq_url,
        cors_origins,
        auth_jwt_public_key_path,
        auth_jwt_private_key_path,
        static_dir,
    })
}
