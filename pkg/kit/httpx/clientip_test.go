package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reqFrom(remote string, xff ...string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8", "192.168.1.7"})
	require.NoError(t, err)

	assert.Equal(t, "203.0.113.9", ClientIP(reqFrom("203.0.113.9:4000", "1.2.3.4"), nil), "no trusted proxy: the peer, whatever the header says")
	assert.Equal(t, "203.0.113.9", ClientIP(reqFrom("203.0.113.9:4000", "1.2.3.4"), trusted), "an untrusted peer cannot vouch for a header")
	assert.Equal(t, "198.51.100.2", ClientIP(reqFrom("10.0.0.5:1", "198.51.100.2"), trusted), "one trusted proxy")
	assert.Equal(t, "198.51.100.2", ClientIP(reqFrom("10.0.0.5:1", "198.51.100.2, 10.1.1.1", "192.168.1.7"), trusted), "a chain of trusted proxies is skipped from the right")
	assert.Equal(t, "198.51.100.2", ClientIP(reqFrom("10.0.0.5:1", "6.6.6.6, 198.51.100.2"), trusted), "a spoofed left-most entry is ignored: the right-most untrusted one wins")
	assert.Equal(t, "10.0.0.5", ClientIP(reqFrom("10.0.0.5:1"), trusted), "no header: the peer")
	assert.Equal(t, "10.0.0.5", ClientIP(reqFrom("10.0.0.5:1", "garbage"), trusted), "an unreadable header is not trusted")
	assert.Equal(t, "2001:db8::1", ClientIP(reqFrom("10.0.0.5:1", "2001:db8::1"), trusted))
}

func TestParseTrustedProxiesRejectsGarbage(t *testing.T) {
	_, err := ParseTrustedProxies([]string{"not-an-ip"})
	require.Error(t, err)
	got, err := ParseTrustedProxies([]string{"", " 10.0.0.1 "})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
