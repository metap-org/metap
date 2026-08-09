//! Per-user platform preferences (`docs/roadmap.md` Phase 14) — today just `locale`, the
//! backend half of i18n: which language a user's UI chrome should render in. Stored
//! server-side (not just client `localStorage`) so it follows the user across devices/logins,
//! same reasoning as `user_roles` living in Postgres rather than on the JWT. Deliberately not
//! metadata-label translation (`EntityField.label` etc. stay single-locale strings for now —
//! that's a separate, much bigger metadata-model decision, see Phase 14's own notes).

use sqlx::PgPool;
use uuid::Uuid;

pub const DEFAULT_LOCALE: &str = "en";

pub async fn get_locale(pool: &PgPool, tenant_id: Uuid, user_id: Uuid) -> anyhow::Result<String> {
    let locale: Option<String> =
        sqlx::query_scalar("SELECT locale FROM user_preferences WHERE tenant_id = $1 AND user_id = $2")
            .bind(tenant_id)
            .bind(user_id)
            .fetch_optional(pool)
            .await?;
    Ok(locale.unwrap_or_else(|| DEFAULT_LOCALE.to_string()))
}

pub async fn set_locale(
    pool: &PgPool,
    tenant_id: Uuid,
    user_id: Uuid,
    locale: &str,
) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO user_preferences (tenant_id, user_id, locale, updated_at) \
         VALUES ($1, $2, $3, now()) \
         ON CONFLICT (tenant_id, user_id) DO UPDATE SET locale = $3, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(user_id)
    .bind(locale)
    .execute(pool)
    .await?;
    Ok(())
}
