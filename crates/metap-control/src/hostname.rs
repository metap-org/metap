//! Which tenant answers on a given hostname (`control.tenant_hostnames`,
//! `crates/migrations/0026_tenant_configs.sql`).
//!
//! This exists for exactly one caller shape: a request that has **no tenant yet**. Everything else
//! in the platform learns its tenant from a verified JWT, so it never needs this. The public theme
//! endpoint (`metap-http`'s `routes/tenant_config.rs`) is served before anyone has logged in, and a
//! `Host` header is all it has to go on.
//!
//! Consequences of that, both deliberate:
//!
//! - The lookup runs against the **platform** pool, not a tenant-routed one. It cannot be
//!   tenant-routed — resolving the route is the whole point of the call — which is why the table
//!   lives in `control` alongside `control.tenants` rather than beside the per-tenant
//!   `tenant_configs` it is used to reach.
//! - The mapping is **operator-written** (`dev-tools set-tenant-hostname`), never tenant-written. A
//!   tenant able to claim an arbitrary hostname could claim a competitor's and serve its own
//!   branding on that tenant's login screen; worse, any future feature that trusts this mapping
//!   would inherit the same hole.
//!
//! A `Host` header is attacker-controlled, so a caller must treat the resolved tenant as a
//! *presentation* hint and never as an authorization fact — see [`normalize_hostname`] for what is
//! stripped before the lookup.

use sqlx::PgExecutor;
use uuid::Uuid;

/// Lowercases, drops a trailing dot and any `:port` suffix, and refuses anything that isn't
/// plausibly a hostname.
///
/// The length cap and the character allowlist are what keep an arbitrary `Host` header from
/// reaching the database as a lookup key at all. Returns `None` rather than a sanitized
/// best-effort: a header this far from a hostname is not a typo to be repaired.
pub fn normalize_hostname(raw: &str) -> Option<String> {
    // An IPv6 literal (`[::1]:8080`) is never a tenant hostname here — reject rather than try to
    // parse brackets, since nothing would ever match anyway.
    if raw.starts_with('[') {
        return None;
    }
    let host = raw.split(':').next()?.trim().trim_end_matches('.').to_ascii_lowercase();
    if host.is_empty() || host.len() > 253 {
        return None;
    }
    if !host.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '.') {
        return None;
    }
    Some(host)
}

/// The tenant registered for `hostname`, or `None` if no tenant claims it.
///
/// A caller must render `None` as "the fleet defaults", never as a 404 — answering differently for a
/// registered and an unregistered hostname turns an unauthenticated endpoint into a tenant-existence
/// oracle.
pub async fn tenant_id_for_hostname<'e>(
    executor: impl PgExecutor<'e>,
    hostname: &str,
) -> Result<Option<Uuid>, sqlx::Error> {
    let Some(host) = normalize_hostname(hostname) else {
        return Ok(None);
    };
    sqlx::query_scalar("SELECT tenant_id FROM control.tenant_hostnames WHERE hostname = $1")
        .bind(host)
        .fetch_optional(executor)
        .await
}

/// Registers (or re-points) a hostname. Operator-only by construction — nothing in the HTTP surface
/// calls this; `dev-tools set-tenant-hostname` is the entry point.
///
/// Rejects a hostname [`normalize_hostname`] won't accept rather than storing a row that could
/// never be matched by a lookup.
pub async fn set_tenant_hostname<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    hostname: &str,
) -> anyhow::Result<String> {
    let host = normalize_hostname(hostname).ok_or_else(|| anyhow::anyhow!("{hostname:?} is not a usable hostname"))?;
    sqlx::query(
        "INSERT INTO control.tenant_hostnames (hostname, tenant_id) VALUES ($1, $2)
         ON CONFLICT (hostname) DO UPDATE SET tenant_id = EXCLUDED.tenant_id",
    )
    .bind(&host)
    .bind(tenant_id)
    .execute(executor)
    .await?;
    Ok(host)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_case_port_and_trailing_dot() {
        assert_eq!(normalize_hostname("App.Example.COM"), Some("app.example.com".into()));
        assert_eq!(
            normalize_hostname("app.example.com:8443"),
            Some("app.example.com".into())
        );
        assert_eq!(normalize_hostname("app.example.com."), Some("app.example.com".into()));
    }

    /// The `Host` header is whatever the client typed. None of these may reach a query.
    #[test]
    fn rejects_anything_that_is_not_a_hostname() {
        for raw in [
            "",
            " ",
            "[::1]:8080",
            "app.example.com/../admin",
            "app.example.com' OR '1'='1",
            "app_example.com",
            "app.example.com ",
        ] {
            let normalized = normalize_hostname(raw);
            assert!(
                normalized.is_none() || normalized.as_deref() == Some("app.example.com"),
                "{raw:?} normalized to {normalized:?}"
            );
        }
        assert_eq!(normalize_hostname("app.example.com' OR '1'='1"), None);
        assert_eq!(normalize_hostname("app_example.com"), None);
        assert_eq!(normalize_hostname(&"a".repeat(254)), None);
    }
}
