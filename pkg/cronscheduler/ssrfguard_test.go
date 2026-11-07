package cronscheduler

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	require.NotNil(t, ip, "invalid test IP %q", s)
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
		assert.True(t, IsBlockedIP(mustParseIP(t, blocked)), "%s should be blocked", blocked)
	}
}

func TestIPv4MappedIPv6DoesNotBypassTheV4Rules(t *testing.T) {
	assert.True(t, IsBlockedIP(mustParseIP(t, "::ffff:169.254.169.254")), "ipv4-mapped metadata address should be blocked")
}

func TestAllowsOrdinaryPublicAddresses(t *testing.T) {
	for _, allowed := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		assert.False(t, IsBlockedIP(mustParseIP(t, allowed)), "%s should be allowed", allowed)
	}
}

func TestRejectsNonHTTPSchemes(t *testing.T) {
	p := WebhookPolicy{}
	for _, raw := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/"} {
		_, err := p.CheckURLShape(raw)
		assert.Error(t, err, "%s should be rejected", raw)
	}
}

func TestEmptyAllowlistPermitsAnyPublicHost(t *testing.T) {
	p := WebhookPolicy{}
	_, err := p.CheckURLShape("https://hooks.example.com/x")
	assert.NoError(t, err, "unexpected error")
}

func TestAllowlistMatchesExactHostOnlyWithoutALeadingDot(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{"api.example.com"}}
	_, err := p.CheckURLShape("https://api.example.com/hook")
	assert.NoError(t, err, "unexpected error")
	_, err = p.CheckURLShape("https://evil.com/hook")
	assert.Error(t, err, "evil.com should be rejected")
	// The prefix-match mistake this guards against: api.example.com.evil.com must not pass.
	_, err = p.CheckURLShape("https://api.example.com.evil.com/hook")
	assert.Error(t, err, "api.example.com.evil.com should be rejected")
}

func TestAllowlistEntryWithALeadingDotMatchesSubdomains(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{".example.com"}}
	_, err := p.CheckURLShape("https://example.com/hook")
	assert.NoError(t, err, "unexpected error")
	_, err = p.CheckURLShape("https://a.b.example.com/hook")
	assert.NoError(t, err, "unexpected error")
	_, err = p.CheckURLShape("https://notexample.com/hook")
	assert.Error(t, err, "notexample.com should be rejected")
}

func TestHostMatchIsCaseInsensitive(t *testing.T) {
	p := WebhookPolicy{AllowedHosts: []string{"api.example.com"}}
	_, err := p.CheckURLShape("https://API.Example.COM/hook")
	assert.NoError(t, err, "unexpected error")
}

func TestForbiddenHeadersAreRejectedRegardlessOfCase(t *testing.T) {
	assert.NoError(t, CheckHeaders([]string{"X-Trace"}), "unexpected error")
	err := CheckHeaders([]string{"Authorization"})
	assert.Error(t, err, "Authorization should be rejected")
	err = CheckHeaders([]string{"cookie"})
	assert.Error(t, err, "cookie should be rejected")
	err = CheckHeaders([]string{"Proxy-Authorization"})
	assert.Error(t, err, "Proxy-Authorization should be rejected")
}

// The A#1 regression for slice 3: adding the Secrets-backed Authorization path must not have
// made a literal one acceptable.
func TestALiteralAuthorizationHeaderStaysRefusedAfterTheSecretPathWasAdded(t *testing.T) {
	for _, spelling := range []string{"authorization", "Authorization", "AUTHORIZATION", " Authorization "} {
		err := CheckHeaders([]string{spelling})
		assert.Error(t, err, "%q must never be settable as a literal header value", spelling)
	}
	err := CheckHeaders([]string{"Cookie"})
	assert.Error(t, err, "Cookie should be rejected")
	err = CheckHeaders([]string{"proxy-authorization"})
	assert.Error(t, err, "proxy-authorization should be rejected")
}

func TestCheckRejectsALiteralMetadataAddressEndToEnd(t *testing.T) {
	p := WebhookPolicy{}
	_, err := p.Check(context.Background(), "http://169.254.169.254/latest/meta-data/")
	assert.Error(t, err, "metadata address should be rejected")
}

func TestCheckAllowsAPrivateTargetWhenExplicitlyOptedIn(t *testing.T) {
	p := WebhookPolicy{AllowPrivateTargets: true}
	_, err := p.Check(context.Background(), "http://10.0.0.5/hook")
	assert.NoError(t, err, "unexpected error")
}
