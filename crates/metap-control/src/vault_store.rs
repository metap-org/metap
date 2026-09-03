//! Second `SecretStore` impl (`docs/roadmap.md` Phase 16 Giai đoạn 4), after `EnvStore`. Static
//! KV v2 secrets over Vault's HTTP API via `vaultrs`. Two auth methods: a plain token
//! (`VaultStore::new`, `VAULT_TOKEN`) and AppRole (`VaultStore::new_with_approle`, added
//! 2026-08-20) — not Vault's dynamic database-credentials engine, still a real gap, left for
//! when a real production deployment target exists to need it
//! (`docs/architectures/07-deployment.md`'s secret-manager note has the same "no target yet"
//! shape). `DbCreds::expires_at` stays `None` here, same as `EnvStore` — nothing in this repo
//! produces a rotating credential yet.
//!
//! AppRole's practical win over a plain token isn't cryptographic strength (a Vault client
//! token is a Vault client token either way) — it's operational: `VAULT_TOKEN` means handing
//! every deployment a long-lived, directly-usable credential to distribute and rotate by hand;
//! AppRole's `role_id` is not secret (safe to bake into a deploy manifest) and its `secret_id`
//! is meant to be issued short-lived/one-time by whatever secrets-injection the deployment
//! already uses (Vault Agent, a CI step, Kubernetes injector), not hand-carried like a raw
//! token.
//!
//! **Auto-renewal (added 2026-08-21, `renew_self`-first fixed the same day after
//! `/code-review` caught the first version consuming a fresh AppRole login on every renewal).**
//! `new_with_approle`'s client token carries a `lease_duration` (`token_ttl` on the AppRole
//! role); every call through [`SecretStore`] checks the remembered expiry first and, whenever
//! less than [`RENEW_BUFFER`] remains, renews *before* making the actual Vault call — no
//! background task, no separate renewal loop, just "check, and renew synchronously if needed"
//! on the request path itself, since `db_credentials`/`put_dsn` calls are already infrequent
//! (`Router` caches a dedicated tenant's opened `PgPool` for 10 minutes, so this isn't hit
//! per-request).
//!
//! Renewal tries `vaultrs::token::renew_self` first — extends the *existing* token's lease
//! without consuming a new AppRole login at all — and only falls back to a fresh
//! `vaultrs::auth::approle::login` (same `role_id`/`secret_id`) if that fails (token exceeded
//! its `max_ttl`, or is otherwise no longer renewable). This matters concretely: this module's
//! own doc comment above recommends provisioning `secret_id` as short-lived/one-time
//! (`secret_id_num_uses=1` on the AppRole role) — a renewal path that always re-logged-in with
//! the same `secret_id` would permanently break itself the very first time it renewed under
//! that exact recommended configuration, since Vault rejects a reused single-use `secret_id`.
//! `renew_self` needs no `secret_id` at all, so it doesn't have this problem.
//!
//! `client`/`expires_at` live behind one `tokio::sync::Mutex`, but only the freshness
//! check-and-maybe-renew runs while holding it — the actual `kv2::read`/`kv2::set` HTTP call
//! (the common case; renewal is rare) runs against a fresh `VaultClient` built just before
//! releasing the lock (same `addr`/current token, via `build_client` — `VaultClient` itself
//! doesn't implement `Clone` upstream, and cloning its fields by hand would mean naming
//! `vaultrs`'s private `rustify`-based HTTP client type, so this rebuilds rather than clones;
//! the cost is a fresh `reqwest::Client`/connection per call instead of reused keep-alive,
//! acceptable since Vault calls aren't a hot path here), so concurrent `db_credentials` calls
//! for different tenants (`Router::dedicated_pool` can trigger several at once — a cold start,
//! or several tenants' cached pools idle-evicted around the same time) don't serialize behind
//! one global lock held for a full network round-trip; only renewal decisions do, which is
//! exactly what needs to be serialized (so two concurrent calls can't both decide to renew and
//! race each other).
//!
//! A `lease_duration` of `0` (Vault's convention for "does not expire") is treated as never
//! needing renewal, not as "already expired" — the difference matters, since the latter would
//! force a renewal attempt on literally every call. Plain-token instances (`VaultStore::new`)
//! have no `role_id`/`secret_id` to renew with and are unaffected (`approle: None`
//! short-circuits the check) — same no-rotation posture that construction path always had,
//! unchanged.

use std::time::{Duration, Instant};

use async_trait::async_trait;
use secrecy::SecretString;
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;
use vaultrs::client::{Client, VaultClient, VaultClientSettingsBuilder};

use crate::secret_store::{DbCreds, SecretStore};

const MOUNT: &str = "secret";
const PATH_PREFIX: &str = "metap/dsn";
/// General secrets live under their own prefix, never mixed in with the DSN tree — a policy an
/// operator writes for one must not accidentally grant the other, and a misrouted read must miss
/// rather than return the wrong kind of credential.
const SECRET_PATH_PREFIX: &str = "metap/secret";
const DEFAULT_APPROLE_MOUNT: &str = "approle";
/// Re-login this long before the current token's remembered expiry, not exactly at it — leaves
/// margin for the login round-trip itself plus any clock/measurement slop. For a short
/// `token_ttl` (well under a minute) this means every call renews, which is correct if
/// unglamorous: better to over-renew a token configured with an unusually short TTL than to
/// let one expire mid-flight.
const RENEW_BUFFER: Duration = Duration::from_secs(60);

struct AppRoleConfig {
    mount: String,
    role_id: String,
    secret_id: String,
}

struct ClientState {
    client: VaultClient,
    /// `None` for a plain-token instance (nothing to renew) or an AppRole token Vault reported
    /// as non-expiring (`lease_duration == 0`); `Some` otherwise.
    expires_at: Option<Instant>,
}

pub struct VaultStore {
    addr: String,
    state: Mutex<ClientState>,
    /// `None` for `VaultStore::new` (plain token) — nothing to re-login with, so
    /// `ensure_fresh_token` is always a no-op for that construction path.
    approle: Option<AppRoleConfig>,
}

fn build_client(addr: &str, token: Option<&str>) -> anyhow::Result<VaultClient> {
    let mut builder = VaultClientSettingsBuilder::default();
    builder.address(addr);
    if let Some(token) = token {
        builder.token(token);
    }
    let settings = builder
        .build()
        .map_err(|e| anyhow::anyhow!("invalid Vault client settings: {e}"))?;
    VaultClient::new(settings).map_err(|e| anyhow::anyhow!("failed to build Vault client: {e}"))
}

fn expiry_from_lease(lease_duration: u64) -> Option<Instant> {
    if lease_duration == 0 {
        None
    } else {
        Some(Instant::now() + Duration::from_secs(lease_duration))
    }
}

impl VaultStore {
    pub fn new(addr: &str, token: &str) -> anyhow::Result<Self> {
        let client = build_client(addr, Some(token))?;
        Ok(Self {
            addr: addr.to_string(),
            state: Mutex::new(ClientState {
                client,
                expires_at: None,
            }),
            approle: None,
        })
    }

    /// AppRole login (`role_id`/`secret_id`, see the module doc comment for why this exists
    /// alongside `new`) — `mount` is the AppRole auth backend's mount path, `"approle"` unless
    /// an operator has mounted it somewhere else.
    pub async fn new_with_approle(addr: &str, mount: &str, role_id: &str, secret_id: &str) -> anyhow::Result<Self> {
        let mut client = build_client(addr, None)?;
        let expires_at = login_approle(&mut client, mount, role_id, secret_id).await?;
        Ok(Self {
            addr: addr.to_string(),
            state: Mutex::new(ClientState { client, expires_at }),
            approle: Some(AppRoleConfig {
                mount: mount.to_string(),
                role_id: role_id.to_string(),
                secret_id: secret_id.to_string(),
            }),
        })
    }

    /// Same as [`Self::new_with_approle`] with the default `"approle"` mount.
    pub async fn new_with_default_approle(addr: &str, role_id: &str, secret_id: &str) -> anyhow::Result<Self> {
        Self::new_with_approle(addr, DEFAULT_APPROLE_MOUNT, role_id, secret_id).await
    }

    /// Renews if less than `RENEW_BUFFER` remains on the remembered expiry — a no-op for a
    /// plain-token instance or a token that's still comfortably fresh. Tries `renew_self` first
    /// (see the module doc comment for why), falls back to a fresh AppRole login only if that
    /// fails. Called at the top of every `SecretStore` method, under the same lock the freshness
    /// check itself runs under, so two concurrent calls can't both decide to renew and race.
    async fn ensure_fresh_token(&self, guard: &mut ClientState) -> anyhow::Result<()> {
        let Some(approle) = &self.approle else {
            return Ok(());
        };
        let needs_renew = match guard.expires_at {
            Some(expires_at) => Instant::now() + RENEW_BUFFER >= expires_at,
            None => false,
        };
        if !needs_renew {
            return Ok(());
        }

        match vaultrs::token::renew_self(&guard.client, None).await {
            Ok(auth_info) => {
                guard.expires_at = expiry_from_lease(auth_info.lease_duration);
                return Ok(());
            }
            Err(e) => {
                tracing::warn!(
                    error = %e,
                    "Vault token renew_self failed, falling back to a fresh AppRole login"
                );
            }
        }

        guard.expires_at = login_approle(&mut guard.client, &approle.mount, &approle.role_id, &approle.secret_id)
            .await
            .map_err(|e| anyhow::anyhow!("AppRole re-login (renewal fallback) failed: {e}"))?;
        Ok(())
    }

    /// Runs `ensure_fresh_token` under the lock, then builds a fresh `VaultClient` (same
    /// `addr`/current token) to make the actual Vault call against *outside* the lock — see the
    /// module doc comment for why (concurrent calls for different tenants shouldn't serialize
    /// behind one lock held for a full network round-trip).
    async fn fresh_client(&self) -> anyhow::Result<VaultClient> {
        let mut guard = self.state.lock().await;
        self.ensure_fresh_token(&mut guard).await?;
        let token = guard.client.settings.token.clone();
        build_client(&self.addr, Some(&token))
    }
}

/// Logs in (or re-logs-in) via AppRole, sets the resulting client token on `client`, and
/// returns what `ClientState::expires_at` should become.
async fn login_approle(
    client: &mut VaultClient,
    mount: &str,
    role_id: &str,
    secret_id: &str,
) -> anyhow::Result<Option<Instant>> {
    let auth_info = vaultrs::auth::approle::login(client, mount, role_id, secret_id)
        .await
        .map_err(|e| anyhow::anyhow!("AppRole login failed against mount {mount}: {e}"))?;
    client.set_token(&auth_info.client_token);
    Ok(expiry_from_lease(auth_info.lease_duration))
}

#[derive(Serialize, Deserialize)]
struct DsnSecret {
    dsn: String,
}

impl VaultStore {
    /// Writes the DSN a `dsn_secret_ref` will resolve to — `dev-tools vault-put-dsn`'s only
    /// caller today (`crates/dev-tools/src/main.rs`), so an operator can populate Vault for a
    /// `dedicated_db` tenant. Not used by `Router`/`db_credentials` (read-only there) — same
    /// read-via-trait/write-via-inherent-method split `PostgresTenantRegistry` already has
    /// between `TenantRegistry::get` and `provision`/`set_status`.
    pub async fn put_dsn(&self, dsn_secret_ref: &str, dsn: &str) -> anyhow::Result<()> {
        let client = self.fresh_client().await?;
        let path = format!("{PATH_PREFIX}/{dsn_secret_ref}");
        vaultrs::kv2::set(&client, MOUNT, &path, &DsnSecret { dsn: dsn.to_string() })
            .await
            .map_err(|e| {
                anyhow::anyhow!("vault kv2 write failed for dsn_secret_ref {dsn_secret_ref} at {MOUNT}/{path}: {e}")
            })?;
        Ok(())
    }
}

#[async_trait]
impl SecretStore for VaultStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        let client = self.fresh_client().await?;
        let path = format!("{PATH_PREFIX}/{dsn_secret_ref}");
        let secret: DsnSecret = vaultrs::kv2::read(&client, MOUNT, &path).await.map_err(|e| {
            anyhow::anyhow!("vault kv2 read failed for dsn_secret_ref {dsn_secret_ref} at {MOUNT}/{path}: {e}")
        })?;
        Ok(DbCreds {
            dsn: SecretString::from(secret.dsn),
            expires_at: None,
        })
    }

    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString> {
        let client = self.fresh_client().await?;
        let path = format!("{SECRET_PATH_PREFIX}/{secret_ref}");
        let secret: crate::secret_store::ValueSecret = vaultrs::kv2::read(&client, MOUNT, &path)
            .await
            .map_err(|e| anyhow::anyhow!("vault kv2 read failed for secret {secret_ref} at {MOUNT}/{path}: {e}"))?;
        Ok(SecretString::from(secret.value))
    }

    async fn put_secret(&self, secret_ref: &str, value: &str) -> anyhow::Result<()> {
        let client = self.fresh_client().await?;
        let path = format!("{SECRET_PATH_PREFIX}/{secret_ref}");
        vaultrs::kv2::set(
            &client,
            MOUNT,
            &path,
            &crate::secret_store::ValueSecret {
                value: value.to_string(),
            },
        )
        .await
        // The error is deliberately built from the path only — `value` must never reach a message
        // that ends up in a log or an HTTP response.
        .map_err(|e| anyhow::anyhow!("vault kv2 write failed for secret {secret_ref} at {MOUNT}/{path}: {e}"))?;
        Ok(())
    }

    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()> {
        let client = self.fresh_client().await?;
        let path = format!("{SECRET_PATH_PREFIX}/{secret_ref}");
        vaultrs::kv2::delete_metadata(&client, MOUNT, &path)
            .await
            .map_err(|e| anyhow::anyhow!("vault kv2 delete failed for secret {secret_ref} at {MOUNT}/{path}: {e}"))?;
        Ok(())
    }
}
