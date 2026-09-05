//! `email` targets — sent via SMTP (`lettre`), auto-appending which record triggered the firing
//! for an `on_transition`/`on_record_event`-triggered job (see `super`'s doc comment).

use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use super::config::SmtpConfig;

#[derive(Deserialize)]
struct EmailConfig {
    to: EmailRecipients,
    subject: String,
    body: String,
}

/// `target_config.to` accepts either a single address or a list — same "string or array of
/// strings" convenience `WebhookConfig.headers`-adjacent shapes elsewhere in this codebase
/// don't need, but a mailing recipient list benefits from.
#[derive(Deserialize)]
#[serde(untagged)]
enum EmailRecipients {
    One(String),
    Many(Vec<String>),
}

impl EmailRecipients {
    fn into_vec(self) -> Vec<String> {
        match self {
            EmailRecipients::One(addr) => vec![addr],
            EmailRecipients::Many(addrs) => addrs,
        }
    }
}

pub(crate) async fn run_email(
    smtp: &SmtpConfig,
    trigger_entity: Option<&str>,
    trigger_record_id: Option<Uuid>,
    target_config: &Value,
) -> anyhow::Result<Value> {
    let cfg: EmailConfig = serde_json::from_value(target_config.clone())?;
    let host = smtp
        .host
        .as_deref()
        .ok_or_else(|| anyhow::anyhow!("email target requires SMTP_HOST to be configured"))?;
    let from = smtp
        .from
        .as_deref()
        .ok_or_else(|| anyhow::anyhow!("email target requires SMTP_FROM to be configured"))?;

    let recipients = cfg.to.into_vec();
    if recipients.is_empty() {
        anyhow::bail!("email target_config.to must not be empty");
    }

    // Auto-append which record actually caused this firing, same intent as `run_webhook`
    // injecting `jobId`/`runId` into its body — `trigger_record_id`/`trigger_entity` are only
    // `Some` for an `on_transition`/`on_record_event`-triggered job (see `CronJobDuePayload`'s
    // doc comment), so a plain `schedule` job's email is left as the admin wrote it.
    let mut body = cfg.body;
    if let Some(entity) = trigger_entity {
        body.push_str(&format!("\n\n---\nTriggered by: {entity}"));
        if let Some(record_id) = trigger_record_id {
            body.push_str(&format!(" (record {record_id})"));
        }
    }

    let mut message_builder = lettre::Message::builder().from(from.parse()?).subject(&cfg.subject);
    for addr in &recipients {
        message_builder = message_builder.to(addr.parse()?);
    }
    let message = message_builder.body(body)?;

    let transport = match (&smtp.user, &smtp.password) {
        (Some(user), Some(password)) => lettre::AsyncSmtpTransport::<lettre::Tokio1Executor>::relay(host)?
            .port(smtp.port)
            .credentials(lettre::transport::smtp::authentication::Credentials::new(
                user.clone(),
                password.clone(),
            ))
            .build(),
        // No credentials — local dev against Mailhog (`docker-compose.yml`'s opt-in `mailhog`
        // service), which speaks plain SMTP with no STARTTLS/auth. `relay()` always requires
        // TLS, so a no-auth target uses the plaintext builder instead.
        _ => lettre::AsyncSmtpTransport::<lettre::Tokio1Executor>::builder_dangerous(host)
            .port(smtp.port)
            .build(),
    };

    lettre::AsyncTransport::send(&transport, message).await?;

    Ok(json!({ "to": recipients, "subject": cfg.subject }))
}
