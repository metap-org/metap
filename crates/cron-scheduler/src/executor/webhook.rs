//! `webhook` targets — calls an external URL, injecting `jobId`/`runId` into a JSON body (see
//! `super`'s doc comment).
//!
//! **The URL, method, headers and body all come from a tenant admin**, so every one of them goes
//! through `super::ssrf_guard` first — see that module's doc comment for the responsive-SSRF this
//! closes (audit 04 finding A#1) and exactly what is blocked by default.
//!
//! Uses its own `reqwest::Client`, not the shared one every other target type gets handed, for one
//! reason the guard can't cover on its own: **redirects**. `reqwest`'s default policy follows up to
//! 10 of them, so an allowed public host answering `302 Location: http://169.254.169.254/` would
//! walk straight past a check that only ever saw the first URL. `redirect::Policy::none()` makes
//! the first hop the only hop; a webhook that legitimately needs to follow one now fails visibly
//! with the 3xx status rather than silently becoming an SSRF primitive.

use std::collections::HashMap;
use std::sync::{Arc, OnceLock};

use secrecy::ExposeSecret;
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use super::ssrf_guard::{check_headers, WebhookPolicy};

/// The config key whose credential a webhook may present
/// (`metap_config::keys::CRON_WEBHOOK_AUTHORIZATION`).
///
/// Spelled as a literal rather than imported so this binary does not take a dependency on
/// `metap-config` for one string — it never reads `tenant_configs` at all. That is not laziness but
/// the design: the reference is *derived* from `(tenant_id, key)`
/// (`metap_control::tenant_secret_ref`), so the executor can resolve a tenant's credential without
/// reading any row a tenant could have influenced, and without this process needing tenant-routed
/// database access it otherwise has no use for.
const WEBHOOK_AUTHORIZATION_KEY: &str = "cron.webhookAuthorization";

#[derive(Deserialize)]
struct WebhookConfig {
    url: String,
    #[serde(default = "default_method")]
    method: String,
    #[serde(default)]
    headers: HashMap<String, String>,
    #[serde(default)]
    body: Option<Value>,
    /// Opt-in: send this tenant's stored credential as the `Authorization` header.
    ///
    /// A boolean, deliberately — not a reference string. A tenant naming the secret to use would be
    /// the exact hole `metap_control::tenant_secret_ref` exists to remove: the reference is derived
    /// from the job's own `tenant_id`, so a job can only ever present its own tenant's credential,
    /// whatever it puts in `target_config`.
    #[serde(default, rename = "authorizationFromSecret")]
    authorization_from_secret: bool,
}

fn default_method() -> String {
    "POST".to_string()
}

/// Built once per process. Separate from the shared client so `redirect::Policy::none()` applies
/// to tenant-supplied URLs only — `workflow_transition`/`bulk_query_action` call an
/// operator-configured base URL and keep the default behavior.
fn client() -> &'static reqwest::Client {
    static CLIENT: OnceLock<reqwest::Client> = OnceLock::new();
    CLIENT.get_or_init(|| {
        reqwest::Client::builder()
            .timeout(metap_runtime::http_client::DEFAULT_TIMEOUT)
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .expect("webhook client config is always valid")
    })
}

#[allow(clippy::too_many_arguments)]
pub(crate) async fn run_webhook(
    policy: &WebhookPolicy,
    secrets: Option<&Arc<dyn metap_control::SecretStore>>,
    tenant_id: Uuid,
    job_id: Uuid,
    run_id: Uuid,
    target_config: &Value,
) -> anyhow::Result<Value> {
    let cfg: WebhookConfig = serde_json::from_value(target_config.clone())?;
    let method = reqwest::Method::from_bytes(cfg.method.as_bytes())
        .map_err(|_| anyhow::anyhow!("invalid HTTP method {:?}", cfg.method))?;

    // Both checks run before any network call: a rejected job fails with a reason the operator
    // can read in `cron_job_runs.error`, having sent nothing.
    check_headers(cfg.headers.keys().map(String::as_str))?;
    let url = policy.check(&cfg.url).await?;

    // **Only now.** The credential is read after the target survived the scheme, IP and allowlist
    // screens, so a secret can never be sent to an address the guard would have rejected. Reversing
    // these two lines would turn this feature back into the credential-forgery primitive audit 04
    // A#1 closed, which is why the ordering is stated here and asserted in `ssrf_guard`'s tests.
    let credential = if cfg.authorization_from_secret {
        let store = secrets.ok_or_else(|| {
            anyhow::anyhow!(
                "this job requests its tenant's stored credential, but no secret backend is configured for \
                 cron-scheduler"
            )
        })?;
        let secret_ref = metap_control::tenant_secret_ref(tenant_id, WEBHOOK_AUTHORIZATION_KEY);
        Some(store.get_secret(&secret_ref).await.map_err(|e| {
            // The reference is safe to name (it is derivable from the tenant id); the value is not,
            // and never reaches this string.
            anyhow::anyhow!("could not resolve this tenant's webhook credential ({secret_ref}): {e}")
        })?)
    } else {
        None
    };

    let mut body = cfg.body.unwrap_or_else(|| json!({}));
    if let Value::Object(map) = &mut body {
        map.insert("jobId".to_string(), json!(job_id));
        map.insert("runId".to_string(), json!(run_id));
    }

    let mut req = client().request(method, url).json(&body);
    for (key, value) in &cfg.headers {
        req = req.header(key, value);
    }
    // Set last, so a tenant header map can never shadow it — `check_headers` already refuses a
    // literal `authorization`, and this ordering means a future loosening there still could not
    // replace the credential with a chosen value.
    if let Some(credential) = &credential {
        req = req.header(reqwest::header::AUTHORIZATION, credential.expose_secret());
    }
    req = metap_runtime::http_client::attach_trace_context(req);

    let response = req.send().await?;
    let status = response.status();
    let text = response.text().await.unwrap_or_default();
    // Everything below reaches `cron_job_runs`, which the tenant admin reads back. An upstream that
    // echoes the request — a debug endpoint, a 401 body quoting what it received — would otherwise
    // hand the credential straight back through that surface.
    let text = redact(text, credential.as_ref().map(|c| c.expose_secret()));
    if !status.is_success() {
        anyhow::bail!("webhook returned {status}: {}", truncate(&text, 500));
    }
    Ok(json!({ "status": status.as_u16(), "body": truncate(&text, 2000) }))
}

/// Replaces every occurrence of the credential just sent with a marker.
///
/// Belt and braces rather than the primary defense — the credential is not supposed to come back at
/// all — but `cron_job_runs` is read by the same tenant admin who can *set* the webhook URL, so
/// "point the job at a server that echoes headers" is a one-step way to read a credential back that
/// the write-only config surface deliberately does not offer. That the tenant owns this credential
/// makes it a small leak rather than a breach; it is still not something to hand over.
///
/// Also covers the empty-secret case explicitly: replacing every empty substring would otherwise
/// shred the whole body.
fn redact(text: String, secret: Option<&str>) -> String {
    match secret {
        Some(secret) if !secret.is_empty() => text.replace(secret, "[REDACTED]"),
        _ => text,
    }
}

/// Truncates on a **char boundary**, not a byte offset. `&s[..max]` panics when `max` lands in the
/// middle of a multi-byte character, and `s` here is a response body from a server the tenant
/// chose — so a webhook answering with UTF-8 text (an accented word, an emoji, any non-ASCII JSON)
/// straddling byte 500 or 2000 would panic the executor task. Found while fixing audit 04 A#1;
/// pre-existing, unrelated to SSRF itself, fixed here because this is the function that handles
/// that body.
fn truncate(s: &str, max: usize) -> String {
    if s.len() <= max {
        return s.to_string();
    }
    let end = s
        .char_indices()
        .map(|(i, _)| i)
        .take_while(|i| *i <= max)
        .last()
        .unwrap_or(0);
    format!("{}...", &s[..end])
}

#[cfg(test)]
mod tests {
    use super::{redact, truncate};

    /// The response body is what a tenant admin reads back from `cron_job_runs`, so a credential
    /// echoed by the upstream must not survive the trip.
    #[test]
    fn redact_removes_an_echoed_credential_from_a_response_body() {
        let body = r#"{"received":{"authorization":"Bearer sk_live_abc123"}}"#.to_string();
        let out = redact(body, Some("Bearer sk_live_abc123"));
        assert!(!out.contains("sk_live_abc123"), "{out}");
        assert!(out.contains("[REDACTED]"), "{out}");
    }

    #[test]
    fn redact_leaves_a_body_alone_when_no_credential_was_sent() {
        let body = r#"{"ok":true}"#.to_string();
        assert_eq!(redact(body.clone(), None), body);
        // An empty secret must not turn into "replace every empty substring", which would insert a
        // marker between every character of the body.
        assert_eq!(redact(body.clone(), Some("")), body);
    }

    use super::WebhookConfig;

    /// A tenant opts in with a boolean; there is no field for naming *which* secret, because the
    /// reference is derived from the job's own tenant. If this ever gains a string field, this test
    /// is what should stop it.
    #[test]
    fn the_config_carries_an_opt_in_flag_and_no_secret_reference() {
        let cfg: WebhookConfig = serde_json::from_value(serde_json::json!({
            "url": "https://api.example.com/hook",
            "authorizationFromSecret": true,
            // A caller trying to name someone else's secret: silently ignored, since no field of
            // `WebhookConfig` can receive it.
            "secretRef": "METAP_TENANT_SOMEONEELSE_CRON_WEBHOOKAUTHORIZATION",
        }))
        .expect("parses");
        assert!(cfg.authorization_from_secret);

        let default: WebhookConfig =
            serde_json::from_value(serde_json::json!({ "url": "https://api.example.com/hook" })).expect("parses");
        assert!(
            !default.authorization_from_secret,
            "a job must not send a credential unless it asked to"
        );
    }

    #[test]
    fn truncate_keeps_short_strings_whole() {
        assert_eq!(truncate("abc", 10), "abc");
    }

    #[test]
    fn truncate_does_not_panic_on_a_multibyte_boundary() {
        // "é" is 2 bytes: cutting at byte 5 lands mid-character, which `&s[..5]` would panic on.
        let s = "abcdéfgh";
        let out = truncate(s, 5);
        assert!(out.ends_with("..."), "{out:?}");
        assert!(s.starts_with(out.trim_end_matches('.')), "{out:?}");
    }

    #[test]
    fn truncate_handles_a_string_that_is_entirely_multibyte() {
        let s = "🙂🙂🙂🙂";
        assert!(truncate(s, 5).ends_with("..."));
    }
}
