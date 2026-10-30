// SSRF guard for webhook.go's tenant-supplied target URL — ported from
// metap-cron-scheduler's executor/ssrf_guard.rs (audit 04 finding A#1).
//
// Why this exists. A "webhook" cron job's target_config (url/method/headers/body) is written by
// a tenant admin — in a SaaS deployment that's a customer, not the operator. Handing that URL
// straight to an HTTP client, and handing the response body back into cron_job_runs (which the
// same admin reads), is a *responsive* SSRF: a customer could read cloud metadata
// (169.254.169.254 -> IAM credentials), reach any service inside the VPC, and set arbitrary
// request headers while doing it.
//
// What is blocked by default, with no configuration: any target that resolves to a
// loopback/private/link-local/CGNAT/unique-local address, any non-http(s) scheme, and the
// Authorization/Cookie/Proxy-Authorization request headers.
//
// What an operator can tighten or loosen (WebhookPolicyFromEnv):
//   - CRON_WEBHOOK_ALLOWED_HOSTS — comma-separated allowlist (api.example.com, or .example.com
//     to match the domain and its subdomains). Unset = allow any public host.
//   - CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS=true — escape hatch for a deployment that genuinely
//     webhooks an internal service.
//
// Known residual limitation — DNS rebinding, same as the Rust original: Check resolves the host
// and rejects blocked addresses, then the real HTTP client resolves it again when it actually
// connects; a DNS record that changes between the two answers could still point the real
// connection at a blocked address. Left open knowingly, same reasoning as the Rust doc comment.
package cronscheduler

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/metap-org/metap-go/runtime"
)

// forbiddenHeaders are request headers a tenant may not set as a literal value on a webhook.
// authorization/proxy-authorization would let a job forge credentials against whatever it
// reaches; cookie does the same for a session-authenticated internal service.
//
// authorization stays on this list even though a webhook may send one via Secrets — that
// credential is resolved by webhook.go *after* the target has passed WebhookPolicy.Check, and is
// never a string the tenant typed into target_config. A literal value here is attacker-chosen
// text going to an attacker-chosen host (credential forgery, still refused unconditionally); a
// Secrets value is the tenant's own credential going to a host that already passed the IP and
// allowlist checks.
var forbiddenHeaders = map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true}

type WebhookPolicy struct {
	// AllowedHosts: empty = any public host allowed. An entry starting with "." matches that
	// domain and any subdomain; anything else must match the host exactly.
	AllowedHosts []string
	// AllowPrivateTargets: false (default) = a target resolving to a
	// private/loopback/link-local/CGNAT/unique-local address is rejected.
	AllowPrivateTargets bool
}

func WebhookPolicyFromEnv() WebhookPolicy {
	var allowedHosts []string
	if raw, ok := runtime.Optional("CRON_WEBHOOK_ALLOWED_HOSTS"); ok {
		for _, h := range strings.Split(raw, ",") {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" {
				allowedHosts = append(allowedHosts, h)
			}
		}
	}
	return WebhookPolicy{
		AllowedHosts:        allowedHosts,
		AllowPrivateTargets: runtime.FlagEnabled("CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS"),
	}
}

// CheckURLShape is the DNS-free half of Check — scheme and host allowlist only. Split out so it
// is unit testable without a resolver, and so the ordering is explicit: shape is rejected before
// any DNS lookup happens.
func (p WebhookPolicy) CheckURLShape(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webhook url scheme %q is not allowed (http/https only)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("webhook url has no host")
	}
	if !p.hostAllowed(host) {
		return nil, fmt.Errorf("webhook host %q is not in CRON_WEBHOOK_ALLOWED_HOSTS", host)
	}
	return u, nil
}

func (p WebhookPolicy) hostAllowed(host string) bool {
	if len(p.AllowedHosts) == 0 {
		return true
	}
	for _, allowed := range p.AllowedHosts {
		if domain, ok := strings.CutPrefix(allowed, "."); ok {
			if host == domain || strings.HasSuffix(host, allowed) {
				return true
			}
		} else if host == allowed {
			return true
		}
	}
	return false
}

// Check is the full check for one outbound webhook: scheme, host allowlist, then every address
// the host resolves to. Returns the parsed URL so the caller doesn't parse it twice.
func (p WebhookPolicy) Check(ctx context.Context, rawURL string) (*url.URL, error) {
	u, err := p.CheckURLShape(rawURL)
	if err != nil {
		return nil, err
	}
	if p.AllowPrivateTargets {
		return u, nil
	}
	host := u.Hostname()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving webhook host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("webhook host %q resolved to no addresses", host)
	}
	for _, addr := range addrs {
		if IsBlockedIP(addr.IP) {
			return nil, fmt.Errorf(
				"webhook target %s resolves to a blocked address (%s) — set CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS=true if this is intentional",
				host, addr.IP)
		}
	}
	return u, nil
}

// CheckHeaders rejects a tenant-supplied header set containing anything in forbiddenHeaders.
func CheckHeaders(names []string) error {
	for _, name := range names {
		lower := strings.ToLower(strings.TrimSpace(name))
		if forbiddenHeaders[lower] {
			return fmt.Errorf("webhook header %q is not allowed", name)
		}
	}
	return nil
}

// IsBlockedIP is every address range that means "inside the deployment" rather than "somewhere
// on the public internet" — the single place this list lives, so unit tests and the runtime
// check can't disagree.
func IsBlockedIP(addr net.IP) bool {
	// net.IP.To4 also unwraps an IPv4-mapped IPv6 address (::ffff:a.b.c.d), so that case is
	// naturally checked as v4 here rather than needing a separate branch the way the Rust
	// original's to_ipv4_mapped() call does explicitly.
	if v4 := addr.To4(); v4 != nil {
		return isBlockedV4(v4)
	}
	return isBlockedV6(addr.To16())
}

func isBlockedV4(ip net.IP) bool {
	a, b := ip[0], ip[1]
	switch {
	case ip.IsPrivate(), ip.IsLoopback(), ip.IsLinkLocalUnicast(), ip.IsUnspecified(), ip.IsMulticast():
		return true
	case ip.Equal(net.IPv4bcast):
		return true
	// 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24 (TEST-NET-1/2/3, Rust's is_documentation()).
	case a == 192 && b == 0 && ip[2] == 2:
		return true
	case a == 198 && b == 51 && ip[2] == 100:
		return true
	case a == 203 && b == 0 && ip[2] == 113:
		return true
	// 100.64.0.0/10, carrier-grade NAT: routable inside many cloud VPCs, not on the public
	// internet.
	case a == 100 && b >= 64 && b < 128:
		return true
	}
	return false
}

func isBlockedV6(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	first := uint16(ip[0])<<8 | uint16(ip[1])
	if first&0xfe00 == 0xfc00 { // fc00::/7 unique-local
		return true
	}
	if first&0xffc0 == 0xfe80 { // fe80::/10 link-local
		return true
	}
	return false
}
