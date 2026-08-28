//! `webhook` targets — calls an arbitrary external URL, injecting `jobId`/`runId` into a JSON
//! body (see `super`'s doc comment).

use std::collections::HashMap;

use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

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

pub(crate) async fn run_webhook(
    http: &reqwest::Client,
    job_id: Uuid,
    run_id: Uuid,
    target_config: &Value,
) -> anyhow::Result<Value> {
    let cfg: WebhookConfig = serde_json::from_value(target_config.clone())?;
    let method = reqwest::Method::from_bytes(cfg.method.as_bytes())
        .map_err(|_| anyhow::anyhow!("invalid HTTP method {:?}", cfg.method))?;

    let mut body = cfg.body.unwrap_or_else(|| json!({}));
    if let Value::Object(map) = &mut body {
        map.insert("jobId".to_string(), json!(job_id));
        map.insert("runId".to_string(), json!(run_id));
    }

    let mut req = http.request(method, &cfg.url).json(&body);
    for (key, value) in &cfg.headers {
        req = req.header(key, value);
    }

    let response = req.send().await?;
    let status = response.status();
    let text = response.text().await.unwrap_or_default();
    if !status.is_success() {
        anyhow::bail!("webhook returned {status}: {}", truncate(&text, 500));
    }
    Ok(json!({ "status": status.as_u16(), "body": truncate(&text, 2000) }))
}

fn truncate(s: &str, max: usize) -> String {
    if s.len() <= max {
        s.to_string()
    } else {
        format!("{}...", &s[..max])
    }
}
