package accountmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvitationEmailCarriesTheLinkAndEncodesTheSubject(t *testing.T) {
	var gotTo string
	var gotMsg []byte
	m := New(Config{Host: "mail", Port: 1025, From: "Portal <no-reply@waf.local>", PublicURL: "http://localhost:5173/"})
	m.send = func(_ context.Context, _ Config, to string, msg []byte) error {
		gotTo, gotMsg = to, msg
		return nil
	}
	link := m.Link("/accept-invite", "a+b/c")
	assert.Equal(t, "http://localhost:5173/accept-invite?token=a%2Bb%2Fc", link, "trailing slash trimmed, token escaped")

	require.NoError(t, m.SendInvitation(context.Background(), Invitation{
		To: "bob@acme.io", TenantName: "Acme", InviterName: "Alice", Link: link,
		ExpiresAt: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC), NewAccount: true,
	}))
	msg := string(gotMsg)
	assert.Equal(t, "bob@acme.io", gotTo)
	assert.Contains(t, msg, "Subject: =?UTF-8?b?")
	assert.Contains(t, msg, link)
	assert.Contains(t, msg, "Alice")
	assert.Contains(t, msg, "2026-11-23 10:00 UTC")
	assert.NotContains(t, strings.ReplaceAll(msg, "\r\n", ""), "\n", "every line ends in CRLF")
}

func TestUnconfiguredMailerSendsNothing(t *testing.T) {
	var nilMailer *Mailer
	assert.False(t, nilMailer.Configured())
	err := New(Config{Host: "mail"}).SendPasswordReset(context.Background(), PasswordReset{To: "x@y.io"})
	assert.True(t, errors.Is(err, ErrNotConfigured), "no SMTP_FROM")
	assert.Equal(t, "/reset-password?token=t", nilMailer.Link("/reset-password", "t"), "relative without PUBLIC_APP_URL")
}
