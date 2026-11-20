//go:build integration

package adminapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/peripherals"
)

func TestSessionListAndRevocation(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	bob := h.member(tenant, "bob@acme.io")
	eve := h.member(tenant, "eve@acme.io")
	now := time.Now()

	// A login session is a row plus a token carrying its id.
	signIn := func(m *identity.Member, ua string) (uuid.UUID, context.Context) {
		sid, err := identity.CreateSession(context.Background(), h.pool, m.IdentityID, m.ID, tenant, time.Hour, "203.0.113.7", ua, now)
		require.NoError(t, err)
		tok, err := peripherals.MintSessionJWT(h.privPEM, tenant, m.ID, 3600, sid.String())
		require.NoError(t, err)
		return sid, CallContext(context.Background(), tok)
	}
	laptop, asLaptop := signIn(bob, "Firefox on Linux")
	phone, asPhone := signIn(bob, "Safari on iPhone")
	_, asEve := signIn(eve, "Chrome")

	list, err := h.client.Account.ListMySessions(asLaptop, &Empty{})
	require.NoError(t, err)
	require.Len(t, list.GetSessions(), 2, "only the caller's own sessions")
	for _, s := range list.GetSessions() {
		assert.Equal(t, s.GetId() == laptop.String(), s.GetCurrent(), "exactly the calling session is current")
		assert.Equal(t, "203.0.113.7", s.GetIp())
		assert.Equal(t, tenant.String(), s.GetTenantId())
	}

	// Someone else's session id looks like a missing one.
	_, err = h.client.Account.RevokeMySession(asEve, &SessionIdRequest{Id: phone.String()})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = h.client.Account.RevokeMySession(asLaptop, &SessionIdRequest{Id: "not-a-uuid"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))

	// Ending the phone's session kills its token at once, and only its token.
	_, err = h.client.Account.GetMe(asPhone, &Empty{})
	require.NoError(t, err)
	_, err = h.client.Account.RevokeMySession(asLaptop, &SessionIdRequest{Id: phone.String()})
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(asPhone, &Empty{})
	assert.Error(t, err, "a revoked session's token stops working at once")
	_, err = h.client.Account.GetMe(asLaptop, &Empty{})
	require.NoError(t, err)
	list, _ = h.client.Account.ListMySessions(asLaptop, &Empty{})
	assert.Len(t, list.GetSessions(), 1)
	_, err = h.client.Account.RevokeMySession(asLaptop, &SessionIdRequest{Id: phone.String()})
	assert.Equal(t, http.StatusNotFound, statusOf(err), "already ended")

	// "Sign out everywhere else" keeps the calling session.
	_, asTablet := signIn(bob, "Chrome on iPad")
	res, err := h.client.Account.RevokeOtherSessions(asLaptop, &Empty{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.GetCount())
	_, err = h.client.Account.GetMe(asTablet, &Empty{})
	assert.Error(t, err)
	_, err = h.client.Account.GetMe(asLaptop, &Empty{})
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(asEve, &Empty{})
	require.NoError(t, err, "another person's session is untouched")

	// A token without a session (API key style / plain) is not a session and keeps working.
	_, err = h.client.Account.GetMe(h.as(tenant, bob.ID), &Empty{})
	require.NoError(t, err)

	// A token whose session row is gone is refused, not trusted.
	ghost, err := peripherals.MintSessionJWT(h.privPEM, tenant, bob.ID, 3600, uuid.NewString())
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(CallContext(context.Background(), ghost), &Empty{})
	assert.Error(t, err)
}
