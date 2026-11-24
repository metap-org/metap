//go:build integration

package httpapi_test

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	accountmail "github.com/metap-org/metap/pkg/auth/mail"
)

// outbox captures what the mailer would have sent.
type outbox struct {
	mu   sync.Mutex
	sent []string
}

func (o *outbox) send(_ context.Context, _ accountmail.Config, to string, msg []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, to+"\n"+string(msg))
	return nil
}

var tokenInLink = regexp.MustCompile(`token=([A-Za-z0-9_%\-]+)`)

// waitToken polls for the newest email to `to` and returns the token of its link.
func (o *outbox) waitToken(t *testing.T, to string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		for i := len(o.sent) - 1; i >= 0; i-- {
			if strings.HasPrefix(o.sent[i], to+"\n") {
				m := tokenInLink.FindStringSubmatch(o.sent[i])
				o.mu.Unlock()
				require.Len(t, m, 2, "the email carries a link")
				tok, err := url.QueryUnescape(m[1])
				require.NoError(t, err)
				return tok
			}
		}
		o.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no email to %s", to)
	return ""
}

func (o *outbox) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.sent)
}

// Roadmap 113 P2: a brand-new account sets its password (under the tenant's policy) when it
// accepts; an existing account proves it is its owner; a link works once.
func TestInvitationAcceptSetsOrConfirmsThePassword(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	acme, beta := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO metadata.tenant_configs (tenant_id, key, value) VALUES ($1, 'auth.passwordMinLength', '12')`, acme)
	require.NoError(t, err)

	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	now := time.Now()

	// A brand-new person, invited to acme (min length 12 there).
	newEmail := uuid.NewString() + "@example.com"
	invited, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: newEmail, Status: identity.StatusInvited})
	require.NoError(t, err)
	token, _, err := identity.CreateInvitation(ctx, pool, acme, invited.ID, invited.IdentityID, true, nil, now)
	require.NoError(t, err)

	res := doRequest(t, "POST", base+"/auth/invitations/lookup", "", map[string]any{"token": token})
	require.Equal(t, 200, res.StatusCode)
	info := decodeJSON(t, res)["data"].(map[string]any)
	assert.Equal(t, "set", info["passwordMode"])
	assert.EqualValues(t, 12, info["minPasswordLength"], "the tenant's policy")
	assert.Equal(t, newEmail, info["email"])

	res = doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{"token": token, "password": "only-ten-c"})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "weak_password", decodeJSON(t, res)["error"].(map[string]any)["code"])

	res = doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{
		"token": token, "password": "twelve-chars-ok", "displayName": "Newbie"})
	require.Equal(t, 200, res.StatusCode)
	assert.NotEmpty(t, res.Cookies(), "accepting signs the person in")
	session := decodeJSON(t, res)["data"].(map[string]any)["token"].(string)
	me := decodeJSON(t, doRequest(t, "GET", base+"/auth/me", session, nil))["data"].(map[string]any)
	assert.Equal(t, invited.ID.String(), me["userId"])
	m, err := identity.GetMember(ctx, pool, acme, invited.ID)
	require.NoError(t, err)
	assert.Equal(t, identity.StatusActive, m.Status)
	assert.Equal(t, "Newbie", m.DisplayName)

	res = doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{"token": token, "password": "twelve-chars-ok"})
	require.Equal(t, 400, res.StatusCode, "a link works once")
	assert.Equal(t, "invitation_invalid", decodeJSON(t, res)["error"].(map[string]any)["code"])

	// Someone who already has an account (in beta) invited to acme: the link alone isn't enough.
	oldEmail := uuid.NewString() + "@example.com"
	existing, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: beta, Email: oldEmail, Password: "existing-password"})
	require.NoError(t, err)
	joining, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: oldEmail, Status: identity.StatusInvited})
	require.NoError(t, err)
	ok, err := identity.CanSetPasswordOnAccept(ctx, pool, joining.IdentityID, joining.ID)
	require.NoError(t, err)
	require.False(t, ok, "an account with a password, in another tenant, is not the inviter's to set")
	token2, _, err := identity.CreateInvitation(ctx, pool, acme, joining.ID, joining.IdentityID, false, nil, now)
	require.NoError(t, err)

	res = doRequest(t, "POST", base+"/auth/invitations/lookup", "", map[string]any{"token": token2})
	assert.Equal(t, "confirm", decodeJSON(t, res)["data"].(map[string]any)["passwordMode"])
	res = doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{"token": token2, "password": "a-new-password-of-my-choice"})
	require.Equal(t, 401, res.StatusCode, "the current password is required, not a new one")
	res = doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{"token": token2, "password": "existing-password"})
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, acme.String(), decodeJSON(t, res)["data"].(map[string]any)["tenantId"])
	ok, err = identity.CheckPassword(ctx, pool, existing.IdentityID, "existing-password")
	require.NoError(t, err)
	assert.True(t, ok, "the existing password is unchanged")

	// A resend revokes the previous link.
	third, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: uuid.NewString() + "@example.com", Status: identity.StatusInvited})
	require.NoError(t, err)
	first, _, err := identity.CreateInvitation(ctx, pool, acme, third.ID, third.IdentityID, true, nil, now)
	require.NoError(t, err)
	_, _, err = identity.CreateInvitation(ctx, pool, acme, third.ID, third.IdentityID, true, nil, now)
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/invitations/lookup", "", map[string]any{"token": first})
	assert.Equal(t, 400, res.StatusCode)
}

// Forgot password: one answer whatever the email, a single-use link, every session revoked.
func TestForgotPasswordResetsThroughTheEmailedLink(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	email := uuid.NewString() + "@example.com"
	_, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: email, Password: "old-password-1"})
	require.NoError(t, err)

	state, _ := newTestAppState(t, pool, httpTestEntity())
	mail := &outbox{}
	state.Mailer = accountmail.New(accountmail.Config{Host: "mail", Port: 1025, From: "no-reply@example.com",
		PublicURL: "http://portal.test"}).WithSender(mail.send)
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/auth/password/forgot", "", map[string]any{"email": "nobody-" + email})
	require.Equal(t, 202, res.StatusCode, "an unknown email gets the same answer")
	res = doRequest(t, "POST", base+"/auth/password/forgot", "", map[string]any{"email": strings.ToUpper(email)})
	require.Equal(t, 202, res.StatusCode)
	token := mail.waitToken(t, email)

	res = doRequest(t, "POST", base+"/auth/password/reset", "", map[string]any{"token": token, "password": "short"})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "weak_password", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res = doRequest(t, "POST", base+"/auth/password/reset", "", map[string]any{"token": token, "password": "new-password-2"})
	require.Equal(t, 204, res.StatusCode)
	res = doRequest(t, "POST", base+"/auth/password/reset", "", map[string]any{"token": token, "password": "new-password-3"})
	require.Equal(t, 400, res.StatusCode, "single use")
	assert.Equal(t, "reset_token_invalid", decodeJSON(t, res)["error"].(map[string]any)["code"])

	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "old-password-1"})
	assert.Equal(t, 401, res.StatusCode)
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "new-password-2"})
	assert.Equal(t, 200, res.StatusCode)

	// At most MaxPasswordResetsPerHour links an hour (one was used above).
	before := mail.count()
	for i := 0; i < identity.MaxPasswordResetsPerHour+2; i++ {
		doRequest(t, "POST", base+"/auth/password/forgot", "", map[string]any{"email": email})
	}
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, identity.MaxPasswordResetsPerHour-1, mail.count()-before)
}
