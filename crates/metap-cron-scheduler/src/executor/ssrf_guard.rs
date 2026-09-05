//! SSRF guard for `super::webhook`'s tenant-supplied target URL — the fix for audit 04 finding
//! A#1 (`../metap-docs/docs/audits/04-auth-protocols-gateway-audit.md`).
//!
//! **Why this exists.** A `webhook` cron job's `target_config` (`url`/`method`/`headers`/`body`)
//! is written by a *tenant admin* through `POST /admin/cron-jobs` — in a SaaS deployment that's a
//! customer, not the operator. Before this module, `run_webhook` handed that URL straight to
//! `reqwest`, and handed the response body back into `cron_job_runs` where the same admin reads
//! it via `GET /admin/cron-jobs/{id}/runs`. That is a *responsive* SSRF: a customer could read
//! cloud metadata (`169.254.169.254` → IAM credentials), reach any service inside the VPC, and
//! set arbitrary request headers while doing it.
//!
//! **What is blocked, by default, with no configuration:** any target that resolves to a
//! loopback/private/link-local/CGNAT/unique-local address, any non-`http(s)` scheme, and the
//! `Authorization`/`Cookie`/`Proxy-Authorization` request headers. That default is what actually
//! stops the attack above (metadata endpoints and internal services are all on those ranges)
//! while leaving a genuine external webhook — the whole point of the feature — working untouched.
//!
//! **The one narrowing since** (`docs/features/18-config-tiers-db-backed.md` slice 3): a job may
//! now carry an `Authorization` header whose value comes from the tenant's own credential in
//! `SecretStore`, never from `target_config`. [`FORBIDDEN_HEADERS`]'s own comment explains why that
//! is a different thing from the literal header this module refuses, and `super::webhook` enforces
//! the ordering that makes it true: the target passes [`WebhookPolicy::check`] *before* the
//! credential is read, so a secret can only ever be sent to a host that already survived the IP
//! and allowlist screens.
//!
//! **What an operator can tighten or loosen** (`WebhookPolicy::from_env`):
//! - `CRON_WEBHOOK_ALLOWED_HOSTS` — comma-separated allowlist (`api.example.com`,
//!   or `.example.com` to match the domain and its subdomains). Unset = allow any *public* host.
//!   Set this for a deployment that wants webhooks to reach only known partners.
//! - `CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS=true` — escape hatch for a deployment that genuinely
//!   webhooks an internal service. Deliberately explicit: turning it on re-opens exactly the
//!   surface this module exists to close, so it must be a decision someone made on purpose.
//!
//! **Deliberately kept local to `cron-scheduler`, not moved to `metap-runtime`.** CLAUDE.md's rule
//! for that crate is ">= 2 real call sites"; `run_webhook` is the only place in the workspace that
//! sends a request to a URL an end user supplied. If a second one ever appears (a `../metap-demo-waf`
//! alerting webhook, say), moving this is the right call then, not now.
//!
//! **Known residual limitation — DNS rebinding.** [`WebhookPolicy::check`] resolves the host and
//! rejects blocked addresses, then `reqwest` resolves it again when it actually connects; a DNS
//! record that changes between the two answers could still point the real connection at a blocked
//! address. Closing that properly means connecting to a pinned, already-validated IP with the
//! original `Host`/SNI preserved, which `reqwest` has no first-class support for. Left open
//! knowingly: it needs an attacker-controlled authoritative DNS server plus a race against a
//! job that fires on a cron schedule (not on demand), which is a large step up in difficulty from
//! "type `http://169.254.169.254/` into a form field".

use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use reqwest::Url;

/// Request headers a tenant may not set **as a literal value** on a webhook.
/// `Authorization`/`Proxy-Authorization` would let a job forge credentials against whatever it
/// reaches; `Cookie` does the same for a session-authenticated internal service. Rejected loudly
/// (the job run fails with this reason) rather than silently dropped — a silently-dropped header
/// looks like the upstream rejecting the call, which is a much worse thing to debug.
///
/// **`authorization` stays on this list even though a webhook may now send one** — see
/// [`check_headers`]. The credential a job sends comes from `SecretStore`, resolved by
/// `super::webhook` *after* the target has passed [`WebhookPolicy::check`], and is never a string
/// the tenant typed into `target_config`. That distinction is the entire reason this can be
/// loosened at all without giving back what audit 04 A#1 closed:
///
/// - A literal value is attacker-chosen text going to an attacker-chosen host, which is credential
///   forgery — still refused, on every target, with or without an allowlist.
/// - A `SecretStore` value is *the tenant's own* credential going to a host that already passed the
///   IP and allowlist checks. It cannot name an internal service (the guard rejected those before
///   the secret was ever read), it cannot be read back by the tenant (`get_secret` has no HTTP
///   surface), and its plaintext never appears in `cron_job_runs` (`super::webhook::redact`).
///
/// `cookie`/`proxy-authorization` have no such path and are refused unconditionally: there is no
/// legitimate case for a scheduled job to present a session cookie or a proxy credential, so
/// nothing is gained by opening them and a session-riding attack is what would be lost.
const FORBIDDEN_HEADERS: [&str; 3] = ["authorization", "cookie", "proxy-authorization"];

#[derive(Debug, Clone, Default)]
pub struct WebhookPolicy {
    /// Empty = any public host allowed. An entry starting with `.` matches that domain and any
    /// subdomain; anything else must match the host exactly.
    pub allowed_hosts: Vec<String>,
    /// `false` (default) = a target resolving to a private/loopback/link-local/CGNAT/ULA address
    /// is rejected.
    pub allow_private_targets: bool,
}

impl WebhookPolicy {
    pub fn from_env() -> Self {
        let allowed_hosts = metap_runtime::env::optional("CRON_WEBHOOK_ALLOWED_HOSTS")
            .map(|raw| {
                raw.split(',')
                    .map(|h| h.trim().to_ascii_lowercase())
                    .filter(|h| !h.is_empty())
                    .collect()
            })
            .unwrap_or_default();
        Self {
            allowed_hosts,
            allow_private_targets: metap_runtime::env::flag_enabled("CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS"),
        }
    }

    /// Full check for one outbound webhook: scheme, host allowlist, then every address the host
    /// resolves to. Returns the parsed `Url` so the caller doesn't parse it twice.
    pub async fn check(&self, raw_url: &str) -> anyhow::Result<Url> {
        let url = self.check_url_shape(raw_url)?;
        if self.allow_private_targets {
            return Ok(url);
        }
        let host = url
            .host_str()
            .ok_or_else(|| anyhow::anyhow!("webhook url has no host"))?;
        let port = url.port_or_known_default().unwrap_or(80);
        for addr in resolve(host, port).await? {
            if is_blocked_ip(addr) {
                anyhow::bail!(
                    "webhook target {host} resolves to a blocked address ({addr}) — set \
                     CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS=true if this is intentional"
                );
            }
        }
        Ok(url)
    }

    /// The DNS-free half of [`check`] — scheme and host allowlist only. Split out so it is unit
    /// testable without a resolver, and so the ordering is explicit: shape is rejected before any
    /// DNS lookup happens, which keeps a malformed or disallowed host from even producing a
    /// resolver query.
    pub fn check_url_shape(&self, raw_url: &str) -> anyhow::Result<Url> {
        let url = Url::parse(raw_url).map_err(|e| anyhow::anyhow!("invalid webhook url {raw_url:?}: {e}"))?;
        if !matches!(url.scheme(), "http" | "https") {
            anyhow::bail!("webhook url scheme {:?} is not allowed (http/https only)", url.scheme());
        }
        let host = url
            .host_str()
            .ok_or_else(|| anyhow::anyhow!("webhook url has no host"))?
            .to_ascii_lowercase();
        if !self.host_allowed(&host) {
            anyhow::bail!("webhook host {host:?} is not in CRON_WEBHOOK_ALLOWED_HOSTS");
        }
        Ok(url)
    }

    fn host_allowed(&self, host: &str) -> bool {
        if self.allowed_hosts.is_empty() {
            return true;
        }
        self.allowed_hosts
            .iter()
            .any(|allowed| match allowed.strip_prefix('.') {
                Some(domain) => host == domain || host.ends_with(allowed),
                None => host == allowed,
            })
    }
}

/// Rejects a tenant-supplied header set containing anything in [`FORBIDDEN_HEADERS`].
pub fn check_headers<'a>(names: impl Iterator<Item = &'a str>) -> anyhow::Result<()> {
    for name in names {
        let lower = name.trim().to_ascii_lowercase();
        if FORBIDDEN_HEADERS.contains(&lower.as_str()) {
            anyhow::bail!("webhook header {name:?} is not allowed");
        }
    }
    Ok(())
}

async fn resolve(host: &str, port: u16) -> anyhow::Result<Vec<IpAddr>> {
    // A bare IP literal in the URL never reaches a resolver, but `lookup_host` handles that case
    // itself (it parses literals before querying), so there is no separate branch for it here.
    let addrs = tokio::net::lookup_host((host, port))
        .await
        .map_err(|e| anyhow::anyhow!("resolving webhook host {host:?}: {e}"))?
        .map(|sa| sa.ip())
        .collect::<Vec<_>>();
    if addrs.is_empty() {
        anyhow::bail!("webhook host {host:?} resolved to no addresses");
    }
    Ok(addrs)
}

/// Every address range that means "inside the deployment" rather than "somewhere on the public
/// internet". `is_blocked_ip` is the single place this list lives, so the unit tests below and the
/// runtime check can't disagree.
pub fn is_blocked_ip(addr: IpAddr) -> bool {
    match addr {
        IpAddr::V4(v4) => is_blocked_v4(v4),
        // An IPv4-mapped v6 address (`::ffff:169.254.169.254`) must be unwrapped and checked as
        // v4 — otherwise it is a one-line bypass of every v4 rule below.
        IpAddr::V6(v6) => match v6.to_ipv4_mapped() {
            Some(v4) => is_blocked_v4(v4),
            None => is_blocked_v6(v6),
        },
    }
}

fn is_blocked_v4(ip: Ipv4Addr) -> bool {
    let [a, b, ..] = ip.octets();
    ip.is_private()
        || ip.is_loopback()
        || ip.is_link_local() // 169.254.0.0/16 — cloud metadata lives here
        || ip.is_unspecified()
        || ip.is_multicast()
        || ip.is_broadcast()
        || ip.is_documentation()
        // 100.64.0.0/10, carrier-grade NAT: routable inside many cloud VPCs, not on the public
        // internet. `Ipv4Addr::is_shared` is still unstable, hence the hand-check.
        || (a == 100 && (64..128).contains(&b))
}

fn is_blocked_v6(ip: Ipv6Addr) -> bool {
    let first = ip.segments()[0];
    ip.is_loopback()
        || ip.is_unspecified()
        || ip.is_multicast()
        // fc00::/7 unique-local and fe80::/10 link-local — both still unstable in std.
        || (first & 0xfe00) == 0xfc00
        || (first & 0xffc0) == 0xfe80
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> WebhookPolicy {
        WebhookPolicy::default()
    }

    #[test]
    fn blocks_cloud_metadata_and_private_ranges() {
        for blocked in [
            "169.254.169.254", // AWS/GCP/Azure metadata
            "127.0.0.1",
            "10.1.2.3",
            "192.168.0.1",
            "172.16.0.1",
            "0.0.0.0",
            "100.64.0.1", // CGNAT
            "::1",
            "fd00::1", // unique-local
            "fe80::1", // link-local
        ] {
            let addr: IpAddr = blocked.parse().unwrap();
            assert!(is_blocked_ip(addr), "{blocked} should be blocked");
        }
    }

    #[test]
    fn ipv4_mapped_ipv6_does_not_bypass_the_v4_rules() {
        let mapped: IpAddr = "::ffff:169.254.169.254".parse().unwrap();
        assert!(is_blocked_ip(mapped));
    }

    #[test]
    fn allows_ordinary_public_addresses() {
        for allowed in ["1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"] {
            let addr: IpAddr = allowed.parse().unwrap();
            assert!(!is_blocked_ip(addr), "{allowed} should be allowed");
        }
    }

    #[test]
    fn rejects_non_http_schemes() {
        for raw in ["file:///etc/passwd", "gopher://example.com/", "ftp://example.com/"] {
            assert!(policy().check_url_shape(raw).is_err(), "{raw} should be rejected");
        }
    }

    #[test]
    fn empty_allowlist_permits_any_public_host() {
        assert!(policy().check_url_shape("https://hooks.example.com/x").is_ok());
    }

    #[test]
    fn allowlist_matches_exact_host_only_without_a_leading_dot() {
        let p = WebhookPolicy {
            allowed_hosts: vec!["api.example.com".to_string()],
            ..Default::default()
        };
        assert!(p.check_url_shape("https://api.example.com/hook").is_ok());
        assert!(p.check_url_shape("https://evil.com/hook").is_err());
        // The prefix-match mistake this guards against: `api.example.com.evil.com` must not pass.
        assert!(p.check_url_shape("https://api.example.com.evil.com/hook").is_err());
    }

    #[test]
    fn allowlist_entry_with_a_leading_dot_matches_subdomains() {
        let p = WebhookPolicy {
            allowed_hosts: vec![".example.com".to_string()],
            ..Default::default()
        };
        assert!(p.check_url_shape("https://example.com/hook").is_ok());
        assert!(p.check_url_shape("https://a.b.example.com/hook").is_ok());
        assert!(p.check_url_shape("https://notexample.com/hook").is_err());
    }

    #[test]
    fn host_match_is_case_insensitive() {
        let p = WebhookPolicy {
            allowed_hosts: vec!["api.example.com".to_string()],
            ..Default::default()
        };
        assert!(p.check_url_shape("https://API.Example.COM/hook").is_ok());
    }

    #[test]
    fn forbidden_headers_are_rejected_regardless_of_case() {
        assert!(check_headers(["X-Trace"].into_iter()).is_ok());
        assert!(check_headers(["Authorization"].into_iter()).is_err());
        assert!(check_headers(["cookie"].into_iter()).is_err());
        assert!(check_headers(["Proxy-Authorization"].into_iter()).is_err());
    }

    /// **The A#1 regression for slice 3.** Adding the `SecretStore`-backed `Authorization` path
    /// must not have made a *literal* one acceptable — that is the exact mistake that would give
    /// back what audit 04 A#1 closed, and it would be an easy one to make while "allowing
    /// Authorization for webhooks".
    #[test]
    fn a_literal_authorization_header_stays_refused_after_the_secret_path_was_added() {
        for spelling in ["authorization", "Authorization", "AUTHORIZATION", " Authorization "] {
            assert!(
                check_headers([spelling].into_iter()).is_err(),
                "{spelling:?} must never be settable as a literal header value"
            );
        }
        // And the two with no legitimate case at all stay refused unconditionally.
        assert!(check_headers(["Cookie"].into_iter()).is_err());
        assert!(check_headers(["proxy-authorization"].into_iter()).is_err());
    }

    #[tokio::test]
    async fn check_rejects_a_literal_metadata_address_end_to_end() {
        let err = policy().check("http://169.254.169.254/latest/meta-data/").await;
        assert!(err.is_err());
    }

    #[tokio::test]
    async fn check_allows_a_private_target_when_explicitly_opted_in() {
        let p = WebhookPolicy {
            allow_private_targets: true,
            ..Default::default()
        };
        assert!(p.check("http://10.0.0.5/hook").await.is_ok());
    }
}
