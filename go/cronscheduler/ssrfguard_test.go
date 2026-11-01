package cronscheduler

import (
	"context"
	"net"
	"testing"
)

func mustParseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("invalid test IP %q", s)
	}
	return ip
}

func TestBlocksCloudMetadataAndPrivateRanges(t *testing.T) {
	for _, blocked := range []string{
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
	} {
		if !IsBlockedIP(mustParseIP(t, blocked)) {
			t.Errorf("%s should be blocked", blocked)
		}
	}
}

func TestIPv4MappedIPv6DoesNotBypassTheV4Rules(t *testing.T) {
	if !IsBlockedIP(mustParseIP(t, "::ffff:169.254.169.254")) {
		t.Error("ipv4-mapped metadata address should be blocked")
	}
}

func TestAllowsOrdinaryPublicAddresses(t *testing.T) {
	for _, allowed := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if IsBlockedIP(mustParseIP(t, allowed)) {
			t.Errorf("%s should be allowed", allowed)
		}
	}
}

func TestRejectsNonHTTPSchemes(t *testing.T) {
	p := WebhookPolicy{}
	for _, raw := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/"} {
		if _, err := p.CheckURLShape(raw); err == nil {
			t.Errorf("%s should be rejected", raw)
		}
	}
}

func TestEmptyAllowlistPermitsAnyPublicHost(t *testing.T) {
	p := WebhookPolicy{}
	if _, err := p.CheckURLShape("https://hooks.example.com/x"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAllowlistMatchesExactHostOnlyWithoutALeadingDot(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{"api.example.com"}}
	if _, err := p.CheckURLShape("https://api.example.com/hook"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := p.CheckURLShape("https://evil.com/hook"); err == nil {
		t.Error("evil.com should be rejected")
	}
	// The prefix-match mistake this guards against: api.example.com.evil.com must not pass.
	if _, err := p.CheckURLShape("https://api.example.com.evil.com/hook"); err == nil {
		t.Error("api.example.com.evil.com should be rejected")
	}
}

func TestAllowlistEntryWithALeadingDotMatchesSubdomains(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{".example.com"}}
	if _, err := p.CheckURLShape("https://example.com/hook"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := p.CheckURLShape("https://a.b.example.com/hook"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := p.CheckURLShape("https://notexample.com/hook"); err == nil {
		t.Error("notexample.com should be rejected")
	}
}

func TestHostMatchIsCaseInsensitive(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{"api.example.com"}}
	if _, err := p.CheckURLShape("https://API.Example.COM/hook"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestForbiddenHeadersAreRejectedRegardlessOfCase(t *testing.T) {
	if err := CheckHeaders([]string{"X-Trace"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := CheckHeaders([]string{"Authorization"}); err == nil {
		t.Error("Authorization should be rejected")
	}
	if err := CheckHeaders([]string{"cookie"}); err == nil {
		t.Error("cookie should be rejected")
	}
	if err := CheckHeaders([]string{"Proxy-Authorization"}); err == nil {
		t.Error("Proxy-Authorization should be rejected")
	}
}

// The A#1 regression for slice 3: adding the Secrets-backed Authorization path must not have
// made a literal one acceptable.
func TestALiteralAuthorizationHeaderStaysRefusedAfterTheSecretPathWasAdded(t *testing.T) {
	for _, spelling := range []string{"authorization", "Authorization", "AUTHORIZATION", " Authorization "} {
		if err := CheckHeaders([]string{spelling}); err == nil {
			t.Errorf("%q must never be settable as a literal header value", spelling)
		}
	}
	if err := CheckHeaders([]string{"Cookie"}); err == nil {
		t.Error("Cookie should be rejected")
	}
	if err := CheckHeaders([]string{"proxy-authorization"}); err == nil {
		t.Error("proxy-authorization should be rejected")
	}
}

func TestCheckRejectsALiteralMetadataAddressEndToEnd(t *testing.T) {
	p := WebhookPolicy{}
	if _, err := p.Check(context.Background(), "http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("metadata address should be rejected")
	}
}

func TestCheckAllowsAPrivateTargetWhenExplicitlyOptedIn(t *testing.T) {
	p := WebhookPolicy{AllowPrivateTargets: true}
	if _, err := p.Check(context.Background(), "http://10.0.0.5/hook"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
