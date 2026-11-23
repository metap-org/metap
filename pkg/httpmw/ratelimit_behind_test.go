package httpmw

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func hit(h http.Handler, remote, xff string) int {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// Behind a trusted gateway every caller shares the gateway's address; the limit must still be
// per caller, or one portal's users would share one bucket.
func TestRateLimitBehindCountsEachCallerSeparately(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	gateway := netip.MustParsePrefix("10.0.0.0/8")
	limited := RateLimitBehind(time.Hour, 1, []netip.Prefix{gateway})(ok)

	assert.Equal(t, 200, hit(limited, "10.0.0.5:1", "198.51.100.1"))
	assert.Equal(t, 429, hit(limited, "10.0.0.5:1", "198.51.100.1"), "the same caller is over its burst")
	assert.Equal(t, 200, hit(limited, "10.0.0.5:1", "198.51.100.2"), "another caller through the same gateway is not")

	plain := RateLimit(time.Hour, 1)(ok)
	assert.Equal(t, 200, hit(plain, "10.0.0.5:1", "198.51.100.1"))
	assert.Equal(t, 429, hit(plain, "10.0.0.5:1", "198.51.100.2"), "with no trusted proxy the peer is the caller, as before")

	spoofed := RateLimitBehind(time.Hour, 1, []netip.Prefix{gateway})(ok)
	assert.Equal(t, 200, hit(spoofed, "203.0.113.7:1", "1.1.1.1"))
	assert.Equal(t, 429, hit(spoofed, "203.0.113.7:1", "2.2.2.2"), "a header from an untrusted peer cannot dodge the limit")
}
