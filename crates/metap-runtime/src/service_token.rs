//! `ServiceTokenSource` — a bearer token for one process's own service account, obtained via a
//! `POST /auth/login` call and kept fresh by a background task, instead of a static, hand-minted-
//! once JWT pasted into `.env`. Moved here 2026-09-02 once a second real caller
//! (`cron-scheduler`, calling REST directly with `reqwest`) needed the exact same logic
//! `graphql-gateway` already had in `metap-grpc::client` for its own gRPC upstream auth — this
//! type itself has no gRPC dependency at all, so `metap-grpc` re-exports it rather than owning it,
//! same as any other `metap-runtime` primitive with >= 2 callers.
//!
//! Found live (2026-09-02, `metap-demo-waf`): a static `UPSTREAM_<N>_SERVICE_JWT` (TTL 3600s,
//! minted once via `dev-tools mint-token`) expired mid-deployment and crashed `graphql-gateway` at
//! boot (schema discovery got a 401). This type replaces that pattern — the credential in `.env`
//! becomes an email+password (doesn't expire, like any other service-account credential) instead
//! of a token (does).

use std::sync::Arc;
use std::time::Duration;

use anyhow::Context as _;
use arc_swap::ArcSwap;
use serde_json::json;

/// How often the background loop logs back in, expressed as a fraction of the server's own token
/// TTL (`metap_http::routes::auth::TOKEN_TTL_SECONDS`, currently 3600s — keep these two in sync if
/// either changes). Refreshing at 2/3 of the TTL leaves a 20-minute buffer for the login endpoint
/// or the network to be down for a while without the cached token actually expiring.
const REFRESH_INTERVAL: Duration = Duration::from_secs(2400);
/// Backoff before retrying a failed background login — short, since `start`'s first login already
/// proved the credentials are valid, so a retry failure is almost always transient (upstream
/// restarting, network blip), not a bad password.
const RETRY_BACKOFF: Duration = Duration::from_secs(30);

#[derive(serde::Deserialize)]
struct LoginResponseEnvelope {
    data: LoginResponseData,
}

#[derive(serde::Deserialize)]
struct LoginResponseData {
    token: String,
}

/// A bearer token for one upstream's service account, obtained via that upstream's own
/// `POST /auth/login` and kept fresh by a background task — the replacement for a static,
/// hand-minted-once JWT (see this module's doc comment for why). Cheap to read (`current()` is a
/// lock-free `ArcSwap` load); logging in again happens only on the background timer or a
/// transient-failure retry, never on the request path.
#[derive(Debug, Clone)]
pub struct ServiceTokenSource {
    current: Arc<ArcSwap<String>>,
}

impl ServiceTokenSource {
    /// Logs in once, synchronously — a failure here fails the caller's boot sequence, same as a
    /// missing/invalid static JWT used to — then spawns the background refresh loop.
    pub async fn start(
        http: reqwest::Client,
        login_url: String,
        email: String,
        password: String,
    ) -> anyhow::Result<Self> {
        let token = login_once(&http, &login_url, &email, &password)
            .await
            .with_context(|| format!("logging into {login_url} as {email}"))?;
        let current = Arc::new(ArcSwap::new(Arc::new(token)));

        let background_current = current.clone();
        tokio::spawn(async move {
            // `next_delay` carries the outcome of the previous attempt into the next sleep — a
            // failed login must retry after `RETRY_BACKOFF`, not after another full
            // `REFRESH_INTERVAL` on top of it (found in review 2026-09-02: the naive "always
            // sleep REFRESH_INTERVAL, then sleep RETRY_BACKOFF once more on error before looping"
            // version effectively retried after RETRY_BACKOFF + REFRESH_INTERVAL, not
            // RETRY_BACKOFF — the exact kind of slow-to-recover bug this whole type exists to
            // avoid).
            let mut next_delay = REFRESH_INTERVAL;
            loop {
                tokio::time::sleep(next_delay).await;
                match login_once(&http, &login_url, &email, &password).await {
                    Ok(token) => {
                        background_current.store(Arc::new(token));
                        next_delay = REFRESH_INTERVAL;
                        tracing::info!(
                            login_url,
                            "refreshed service account token; next refresh in {REFRESH_INTERVAL:?}"
                        );
                    }
                    Err(e) => {
                        next_delay = RETRY_BACKOFF;
                        tracing::error!(login_url, error = %e, "failed to refresh service account token, keeping previous one; retrying in {RETRY_BACKOFF:?}");
                    }
                }
            }
        });

        Ok(Self { current })
    }

    /// A fixed, never-refreshing token — for tests that don't have a real `/auth/login` server to
    /// log into (they mint a JWT directly against their own throwaway keypair instead).
    pub fn from_static(token: impl Into<String>) -> Self {
        Self {
            current: Arc::new(ArcSwap::new(Arc::new(token.into()))),
        }
    }

    pub fn current(&self) -> Arc<String> {
        self.current.load_full()
    }
}

async fn login_once(http: &reqwest::Client, login_url: &str, email: &str, password: &str) -> anyhow::Result<String> {
    let response: LoginResponseEnvelope = http
        .post(login_url)
        .json(&json!({ "email": email, "password": password }))
        .send()
        .await
        .with_context(|| format!("POST {login_url}"))?
        .error_for_status()
        .with_context(|| format!("{login_url} returned an error status"))?
        .json()
        .await
        .with_context(|| format!("parsing {login_url} response"))?;
    Ok(response.data.token)
}
