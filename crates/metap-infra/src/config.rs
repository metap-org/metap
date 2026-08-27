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
    /// Opt-in — when set, `apps/crm-server/src/main.rs` builds a `metap_control::VaultStore`
    /// instead of the default `EnvStore` for resolving `DedicatedDb` tenant DSNs
    /// (`docs/roadmap.md` Phase 16 Giai đoạn 4). Two auth methods, picked by which vars are
    /// present alongside this one: `vault_token` (plain token) or `vault_role_id` +
    /// `vault_secret_id` (AppRole, added 2026-08-20 — see `VaultStore`'s doc comment for why
    /// both exist). Neither is validated here (no format requirement) — `VaultStore::new`/
    /// `new_with_approle` surface a clear error at construction if `vault_addr` isn't a usable
    /// Vault address or the credentials are rejected.
    pub vault_addr: Option<String>,
    /// See `vault_addr`. Takes precedence over `vault_role_id`/`vault_secret_id` if both forms
    /// are somehow set — an operator picks one auth method, not both.
    pub vault_token: Option<String>,
    /// See `vault_addr`. AppRole `role_id` — not secret, safe to bake into a deploy manifest.
    pub vault_role_id: Option<String>,
    /// See `vault_addr`. AppRole `secret_id` — meant to be short-lived/one-time, injected by
    /// whatever secrets pipeline the deployment already uses, not hand-carried like
    /// `vault_token`.
    pub vault_secret_id: Option<String>,
    /// See `vault_addr`. AppRole auth backend mount path — defaults to `"approle"` (Vault's own
    /// default) when `vault_role_id`/`vault_secret_id` are set but this isn't.
    pub vault_approle_mount: Option<String>,
    /// Opt-in, alternative to Vault — when set, `metap_control::build_secret_store` builds an
    /// `AwsSecretsManagerStore` instead of `VaultStore`/`EnvStore` (`docs/roadmap.md` Phase 8,
    /// cloud secret-manager target). Takes precedence over Vault if both `aws_secrets_region`
    /// and `vault_addr` are somehow set — see `build_secret_store`'s own doc comment for the
    /// full precedence order across all four backends.
    pub aws_secrets_region: Option<String>,
    /// See `aws_secrets_region`. Required alongside it — `AwsSecretsManagerStore` uses explicit
    /// credentials, not the SDK's default provider chain (see that type's doc comment for why).
    pub aws_secrets_access_key: Option<String>,
    /// See `aws_secrets_region`.
    pub aws_secrets_secret_key: Option<String>,
    /// See `aws_secrets_region`. LocalStack or another AWS-API-compatible test double — unset
    /// for real AWS Secrets Manager.
    pub aws_secrets_endpoint_url: Option<String>,
    /// Opt-in, alternative to Vault/AWS — when set, `metap_control::build_secret_store` builds a
    /// `GcpSecretManagerStore` (`docs/roadmap.md` Phase 8). No access-key/secret-key pair here —
    /// GCP resolves identity via Application Default Credentials (see that type's doc comment).
    /// Takes precedence over both AWS and Vault if more than one is somehow configured at once.
    pub gcp_secrets_project_id: Option<String>,
    /// Opt-in — when set, names the entity `metap-http`'s `AuthContext` extractor reads a
    /// caller's own record from (matched by a `userId` field) to enrich `RequestContext` with
    /// attributes beyond identity/role, e.g. `"hr.employees"` so a `departmentId` field becomes
    /// readable via `PolicyCondition`'s `fromContext` (`docs/features/03-organization-identity.md`).
    /// `None` (default) is a full no-op — no extra query, no behavior change.
    pub auth_context_entity: Option<String>,
    /// See `auth_context_entity`. How long a resolved (or absent) result stays cached
    /// (`metap_http::cache::ContextAttributesCache`) before the next request re-queries —
    /// unlike role lookup (always fresh, never cached), this is deliberately cached since it
    /// reads an ordinary business record, not a security-critical role assignment.
    /// `POST /admin/users/{userId}/context/invalidate` clears an entry immediately instead of
    /// waiting out this window. Defaults to 30s, matching `metap-control::RegistryCache`.
    pub auth_context_cache_ttl_seconds: u64,
    /// Opt-in — when set, `PermissionService` (`crates/metap-permission`) caches policy-row
    /// lookups in `metap-cache::RedisCache` (Redis/DragonflyDB/Valkey, whatever's at this URL)
    /// instead of holding no cache at all. Unset (default) means no policy caching — every
    /// `load_snapshot` call queries `PolicyStore` fresh, exactly as before this cache existed.
    /// Distributed rather than `MokaCache` by default because policy data must stay consistent
    /// across every server instance behind a load balancer, not just the instance that happened
    /// to serve the write (`docs/architectures/07-deployment.md`'s multi-instance gap).
    pub policy_cache_redis_url: Option<String>,
    /// See `policy_cache_redis_url`. Defaults to 30s, matching `auth_context_cache_ttl_seconds`'s
    /// convention — policy rows are admin-edited config, not per-request state.
    pub policy_cache_ttl_seconds: u64,
    /// SMTP config for `metap-cron`'s `TargetType::Email` target (`cron-scheduler`'s `run_email`,
    /// Phase 39) — all optional, since only `cron-scheduler` needs them and only once an admin
    /// actually creates an `email` job; unset means that job fails clearly at execution time
    /// (`run_email` errors with a message naming the missing config) rather than at boot.
    /// Local dev points these at Mailhog (`docker-compose.yml`'s opt-in `mailhog` service,
    /// `localhost:1025`, no auth) rather than a real provider account.
    pub smtp_host: Option<String>,
    pub smtp_port: u16,
    pub smtp_user: Option<String>,
    pub smtp_password: Option<String>,
    /// The `From:` address `run_email` sends as — required alongside `smtp_host` for a job to
    /// actually run (a `From:` address isn't optional in SMTP the way auth can be, e.g. Mailhog).
    pub smtp_from: Option<String>,
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

    let vault_addr = env::var("VAULT_ADDR").ok().filter(|s| !s.is_empty());
    let vault_token = env::var("VAULT_TOKEN").ok().filter(|s| !s.is_empty());
    let vault_role_id = env::var("VAULT_ROLE_ID").ok().filter(|s| !s.is_empty());
    let vault_secret_id = env::var("VAULT_SECRET_ID").ok().filter(|s| !s.is_empty());
    let vault_approle_mount = env::var("VAULT_APPROLE_MOUNT").ok().filter(|s| !s.is_empty());

    let aws_secrets_region = env::var("AWS_SECRETS_REGION").ok().filter(|s| !s.is_empty());
    let aws_secrets_access_key = env::var("AWS_SECRETS_ACCESS_KEY").ok().filter(|s| !s.is_empty());
    let aws_secrets_secret_key = env::var("AWS_SECRETS_SECRET_KEY").ok().filter(|s| !s.is_empty());
    let aws_secrets_endpoint_url = env::var("AWS_SECRETS_ENDPOINT_URL").ok().filter(|s| !s.is_empty());
    let gcp_secrets_project_id = env::var("GCP_SECRETS_PROJECT_ID").ok().filter(|s| !s.is_empty());

    let auth_context_entity = env::var("AUTH_CONTEXT_ENTITY").ok().filter(|s| !s.is_empty());
    let auth_context_cache_ttl_seconds: u64 = env::var("AUTH_CONTEXT_CACHE_TTL_SECONDS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(30);

    let policy_cache_redis_url = env::var("POLICY_CACHE_REDIS_URL").ok().filter(|s| !s.is_empty());
    let policy_cache_ttl_seconds: u64 = env::var("POLICY_CACHE_TTL_SECONDS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(30);

    let smtp_host = env::var("SMTP_HOST").ok().filter(|s| !s.is_empty());
    let smtp_port: u16 = env::var("SMTP_PORT").ok().and_then(|v| v.parse().ok()).unwrap_or(1025); // Mailhog's default SMTP port
    let smtp_user = env::var("SMTP_USER").ok().filter(|s| !s.is_empty());
    let smtp_password = env::var("SMTP_PASSWORD").ok().filter(|s| !s.is_empty());
    let smtp_from = env::var("SMTP_FROM").ok().filter(|s| !s.is_empty());

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
        vault_addr,
        vault_token,
        vault_role_id,
        vault_secret_id,
        vault_approle_mount,
        aws_secrets_region,
        aws_secrets_access_key,
        aws_secrets_secret_key,
        aws_secrets_endpoint_url,
        gcp_secrets_project_id,
        auth_context_entity,
        auth_context_cache_ttl_seconds,
        policy_cache_redis_url,
        policy_cache_ttl_seconds,
        smtp_host,
        smtp_port,
        smtp_user,
        smtp_password,
        smtp_from,
    })
}
