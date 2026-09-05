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

/// How long [`ServiceTokenSource::start`]'s *initial* login may keep retrying a transient
/// connection failure before giving up and failing the caller's boot sequence for real. Found live
/// (2026-09-05, `metap-demo-waf`'s dev compose): `graphql-gateway` and its upstreams all
/// path-depend on this repo's crates and rebuild independently via `cargo watch`, so the gateway
/// can genuinely race an upstream that hasn't finished restarting yet — a plain connection-refused,
/// not a real configuration problem, but it crashed the gateway's boot outright before this existed.
const INITIAL_LOGIN_RETRY_BUDGET: Duration = Duration::from_secs(60);
/// How often the initial login retries within that budget — short, since the whole point is riding
/// out a few seconds of "upstream still starting", not waiting out a real outage.
const INITIAL_LOGIN_RETRY_INTERVAL: Duration = Duration::from_secs(2);

#[derive(serde::Deserialize)]
struct LoginResponseEnvelope {
    data: LoginResponseData,
}

#[derive(serde::Deserialize)]
struct LoginResponseData {
    token: String,
}

/// [`login_once`]'s result, distinguishing a transport-level failure (the upstream isn't reachable
/// at all — connection refused, DNS not resolving yet, a timeout) from a definite answer the
/// upstream gave us (a real HTTP error status, or a response body that doesn't parse). Only the
/// former is worth retrying: a 401 means the credentials are actually wrong, and retrying that for
/// a minute would just delay a real misconfiguration from being reported.
enum LoginOutcome {
    Ok(String),
    Retryable(anyhow::Error),
    Fatal(anyhow::Error),
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
    /// Logs in, retrying a transient connection failure for up to [`INITIAL_LOGIN_RETRY_BUDGET`]
    /// (`LoginOutcome::Retryable`) — but failing the caller's boot sequence immediately on a
    /// definite answer from the upstream (`LoginOutcome::Fatal`: bad credentials, a real 5xx, an
    /// unparseable response), same as a missing/invalid static JWT used to. Then spawns the
    /// background refresh loop.
    pub async fn start(
        http: reqwest::Client,
        login_url: String,
        email: String,
        password: String,
    ) -> anyhow::Result<Self> {
        let deadline = tokio::time::Instant::now() + INITIAL_LOGIN_RETRY_BUDGET;
        let token = loop {
            match login_once(&http, &login_url, &email, &password).await {
                LoginOutcome::Ok(token) => break token,
                LoginOutcome::Fatal(e) => {
                    return Err(e.context(format!("logging into {login_url} as {email}")));
                }
                LoginOutcome::Retryable(e) => {
                    if tokio::time::Instant::now() >= deadline {
                        return Err(e.context(format!(
                            "logging into {login_url} as {email} (gave up after {INITIAL_LOGIN_RETRY_BUDGET:?} of retries)"
                        )));
                    }
                    tracing::warn!(
                        login_url,
                        error = %e,
                        "upstream not reachable yet, retrying in {INITIAL_LOGIN_RETRY_INTERVAL:?}"
                    );
                    tokio::time::sleep(INITIAL_LOGIN_RETRY_INTERVAL).await;
                }
            }
        };
        let current = Arc::new(ArcSwap::new(Arc::new(token)));

        let background_current = current.clone();
        tokio::spawn(async move {
            // `next_delay` carries the outcome of the previous attempt into the next sleep — a
            // failed login must retry after `RETRY_BACKOFF`, not after another full
            // `REFRESH_INTERVAL` on top of it (found in review 2026-09-02: the naive "always
            // sleep REFRESH_INTERVAL, then sleep RETRY_BACKOFF once more on error before looping"
            // version effectively retried after RETRY_BACKOFF + REFRESH_INTERVAL, not
            // RETRY_BACKOFF — the exact kind of slow-to-recover bug this whole type exists to
            // avoid). Unlike the initial login above, `Retryable` and `Fatal` are treated the same
            // way here — the first login already proved the credentials are valid, so *any*
            // failure this far in is presumed transient (see `RETRY_BACKOFF`'s own doc comment).
            let mut next_delay = REFRESH_INTERVAL;
            loop {
                tokio::time::sleep(next_delay).await;
                match login_once(&http, &login_url, &email, &password).await {
                    LoginOutcome::Ok(token) => {
                        background_current.store(Arc::new(token));
                        next_delay = REFRESH_INTERVAL;
                        tracing::info!(
                            login_url,
                            "refreshed service account token; next refresh in {REFRESH_INTERVAL:?}"
                        );
                    }
                    LoginOutcome::Retryable(e) | LoginOutcome::Fatal(e) => {
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

async fn login_once(http: &reqwest::Client, login_url: &str, email: &str, password: &str) -> LoginOutcome {
    let response = match http
        .post(login_url)
        .json(&json!({ "email": email, "password": password }))
        .send()
        .await
    {
        Ok(response) => response,
        Err(e) => {
            // No response at all — the upstream isn't there to have answered, which is exactly
            // the boot-race case worth retrying. Anything else (a redirect loop, an invalid URL)
            // is a real configuration bug, not a race, so it stays fatal.
            let retryable = e.is_connect() || e.is_timeout();
            let err = anyhow::Error::new(e).context(format!("POST {login_url}"));
            return if retryable {
                LoginOutcome::Retryable(err)
            } else {
                LoginOutcome::Fatal(err)
            };
        }
    };
    let response = match response.error_for_status() {
        Ok(response) => response,
        // A real HTTP status came back — the upstream is up and answered, so this is never a
        // boot race. Most commonly a 401 (wrong service-account password), which retrying would
        // only delay reporting.
        Err(e) => {
            return LoginOutcome::Fatal(anyhow::Error::new(e).context(format!("{login_url} returned an error status")))
        }
    };
    match response.json::<LoginResponseEnvelope>().await {
        Ok(body) => LoginOutcome::Ok(body.data.token),
        Err(e) => LoginOutcome::Fatal(anyhow::Error::new(e).context(format!("parsing {login_url} response"))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::routing::post;
    use axum::Router;
    use std::net::SocketAddr;
    use tokio::net::TcpListener;

    async fn ok_login() -> axum::Json<serde_json::Value> {
        axum::Json(json!({ "data": { "token": "fake-token" } }))
    }

    async fn unauthorized_login() -> (axum::http::StatusCode, axum::Json<serde_json::Value>) {
        (
            axum::http::StatusCode::UNAUTHORIZED,
            axum::Json(json!({ "error": { "code": "invalid_credentials", "message": "nope" } })),
        )
    }

    async fn serve(router: Router, addr: SocketAddr) {
        let listener = TcpListener::bind(addr).await.expect("bind test server");
        axum::serve(listener, router).await.expect("test server crashed");
    }

    /// A definite 401 must fail immediately, not retry for `INITIAL_LOGIN_RETRY_BUDGET` — a wrong
    /// service-account password is never going to fix itself by waiting.
    #[tokio::test]
    async fn fails_fast_on_a_real_unauthorized_response() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        drop(listener);
        tokio::spawn(serve(
            Router::new().route("/auth/login", post(unauthorized_login)),
            addr,
        ));
        // Give the router a moment to actually bind before hitting it.
        tokio::time::sleep(Duration::from_millis(50)).await;

        let started = tokio::time::Instant::now();
        let result = ServiceTokenSource::start(
            reqwest::Client::new(),
            format!("http://{addr}/auth/login"),
            "svc@internal.local".to_string(),
            "wrong".to_string(),
        )
        .await;

        assert!(result.is_err());
        assert!(
            started.elapsed() < INITIAL_LOGIN_RETRY_BUDGET,
            "a definite 401 must not go through the retry budget"
        );
    }

    /// The boot-race case this whole change exists for: the upstream isn't listening yet at the
    /// first attempt (nothing bound to `addr`), then comes up a moment later — `start` must ride
    /// that out and still succeed, rather than failing on the first connection-refused.
    #[tokio::test]
    async fn retries_a_connection_refused_until_the_upstream_comes_up() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        drop(listener); // nothing listens here yet — the first login attempt(s) must see connection-refused

        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(300)).await;
            serve(Router::new().route("/auth/login", post(ok_login)), addr).await;
        });

        let token = ServiceTokenSource::start(
            reqwest::Client::new(),
            format!("http://{addr}/auth/login"),
            "svc@internal.local".to_string(),
            "correct".to_string(),
        )
        .await
        .expect("should ride out the boot race and eventually log in");

        assert_eq!(*token.current(), "fake-token");
    }
}
