package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Session cookies are Secure unless somebody turns it off on purpose: unset, empty and a value that
// is not understood all keep it on.
func TestCookiesAreSecureUnlessTurnedOff(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("RABBITMQ_URL", "amqp://u:p@localhost:5672")
	t.Setenv("JWKS_URL", "http://issuer/.well-known/jwks.json")
	for value, want := range map[string]bool{"": true, "true": true, "yes": true, "fals": true, "false": false, "FALSE": false, " 0 ": false} {
		t.Setenv("COOKIE_SECURE", value)
		cfg, err := LoadApp()
		require.NoError(t, err)
		assert.Equal(t, want, cfg.CookieSecure, "COOKIE_SECURE=%q", value)
	}
}
