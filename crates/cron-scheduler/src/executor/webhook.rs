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
use std::sync::OnceLock;

use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use super::ssrf_guard::{check_headers, WebhookPolicy};

#[derive(Deserialize)]
struct WebhookConfig {
    url: String,
    #[serde(default = "default_method")]
    method: String,
    #[serde(default)]
    headers: HashMap<String, String>,
    #[serde(default)]
    body: Option<Value>,
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

pub(crate) async fn run_webhook(
    policy: &WebhookPolicy,
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

    let mut body = cfg.body.unwrap_or_else(|| json!({}));
    if let Value::Object(map) = &mut body {
        map.insert("jobId".to_string(), json!(job_id));
        map.insert("runId".to_string(), json!(run_id));
    }

    let mut req = client().request(method, url).json(&body);
    for (key, value) in &cfg.headers {
        req = req.header(key, value);
    }
    req = metap_runtime::http_client::attach_trace_context(req);

    let response = req.send().await?;
    let status = response.status();
    let text = response.text().await.unwrap_or_default();
    if !status.is_success() {
        anyhow::bail!("webhook returned {status}: {}", truncate(&text, 500));
    }
    Ok(json!({ "status": status.as_u16(), "body": truncate(&text, 2000) }))
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
    use super::truncate;

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
