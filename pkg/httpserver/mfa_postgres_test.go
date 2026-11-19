//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/peripherals"
)

// Roadmap 115 M3: a person with a second factor signs in in two steps, a code works once, a wrong
// code burns the challenge, recovery codes are single use, and a password-only path (HTTP Basic's
// verifier) refuses them rather than bypassing the second step.
func TestLoginWithSecondFactor(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	email := uuid.NewString() + "@example.com"
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: email, Password: "first-password"})
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_challenges WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_recovery_codes WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_factors WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE id = $1", m.IdentityID)
	})
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	login := func() map[string]any {
		res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "first-password"})
		require.Equal(t, 200, res.StatusCode)
		return decodeJSON(t, res)["data"].(map[string]any)
	}

	// No second factor yet: a plain session.
	assert.NotEmpty(t, login()["token"])

	// Enrol (what AccountService does), confirm with a real code.
	secret, _, err := identity.BeginMFAEnrollment(ctx, pool, m.IdentityID, email, time.Now())
	require.NoError(t, err)
	code, err := identity.TOTPAt(secret, time.Now())
	require.NoError(t, err)
	recovery, err := identity.ConfirmMFAEnrollment(ctx, pool, m.IdentityID, code, time.Now())
	require.NoError(t, err)
	require.Len(t, recovery, 10)

	// The password alone no longer gives a session.
	step1 := login()
	require.Equal(t, true, step1["mfaRequired"])
	assert.Nil(t, step1["token"])
	mfaToken := step1["mfaToken"].(string)

	// A wrong code is refused; the right one then signs in. The code just used at enrolment is
	// refused (single use), so wait for the next step by asking for the previous/next window.
	res := doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": mfaToken, "code": "000000"})
	require.Equal(t, 401, res.StatusCode)
	assert.Equal(t, "mfa_invalid_code", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": mfaToken, "code": code})
	assert.Equal(t, 401, res.StatusCode, "the code that confirmed enrolment cannot be replayed")
	next, err := identity.TOTPAt(secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": mfaToken, "code": next})
	require.Equal(t, 200, res.StatusCode)
	token := decodeJSON(t, res)["data"].(map[string]any)["token"].(string)
	me := decodeJSON(t, doRequest(t, "GET", base+"/auth/me", token, nil))["data"].(map[string]any)
	assert.Equal(t, m.ID.String(), me["userId"])

	// The challenge is used up.
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": mfaToken, "code": next})
	assert.Equal(t, 401, res.StatusCode)
	assert.Equal(t, "mfa_challenge", decodeJSON(t, res)["error"].(map[string]any)["code"])

	// Five wrong codes kill a challenge, even if the sixth would be right.
	burn := login()["mfaToken"].(string)
	for i := 0; i < identity.MaxChallengeTry; i++ {
		doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": burn, "code": "111111"})
	}
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": burn, "code": recovery[0]})
	assert.Equal(t, 401, res.StatusCode)

	// A recovery code works once.
	rc := login()["mfaToken"].(string)
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": rc, "code": recovery[0]})
	require.Equal(t, 200, res.StatusCode)
	rc = login()["mfaToken"].(string)
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": rc, "code": recovery[0]})
	assert.Equal(t, 401, res.StatusCode, "a recovery code is single use")

	// A path with only a password (HTTP Basic and what is built on it) refuses the account.
	u, err := peripherals.VerifyCredentials(ctx, pool, email, "first-password", &tenant)
	require.NoError(t, err)
	assert.Nil(t, u)

	// An admin reset turns it off again.
	require.NoError(t, identity.DisableMFA(ctx, pool, m.IdentityID))
	assert.NotEmpty(t, login()["token"])
}

// A tenant that requires MFA makes a person without it set it up before getting a session.
func TestTenantRequiringMFAForcesSetup(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	email := uuid.NewString() + "@example.com"
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: email, Password: "first-password"})
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_challenges WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_recovery_codes WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.mfa_factors WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.tenant_configs WHERE tenant_id = $1", tenant)
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE id = $1", m.IdentityID)
	})
	state, _ := newTestAppState(t, pool, httpTestEntity())
	require.NoError(t, state.Config.SetTenant(ctx, pool, tenant, config.AuthRequireMFA, json.RawMessage("true")))
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "first-password"})
	require.Equal(t, 200, res.StatusCode)
	data := decodeJSON(t, res)["data"].(map[string]any)
	require.Equal(t, true, data["mfaSetupRequired"])
	assert.Nil(t, data["token"])
	mfaToken := data["mfaToken"].(string)

	// A sign-in challenge is not a setup challenge and vice versa.
	res = doRequest(t, "POST", base+"/auth/login/mfa", "", map[string]any{"mfaToken": mfaToken, "code": "123456"})
	assert.Equal(t, 401, res.StatusCode)

	res = doRequest(t, "POST", base+"/auth/mfa/setup", "", map[string]any{"mfaToken": mfaToken})
	require.Equal(t, 200, res.StatusCode)
	setup := decodeJSON(t, res)["data"].(map[string]any)
	assert.Contains(t, setup["otpauthUri"], "otpauth://totp/")
	code, err := identity.TOTPAt(setup["secret"].(string), time.Now())
	require.NoError(t, err)

	res = doRequest(t, "POST", base+"/auth/mfa/setup/confirm", "", map[string]any{"mfaToken": mfaToken, "code": "000000"})
	require.Equal(t, 401, res.StatusCode)
	res = doRequest(t, "POST", base+"/auth/mfa/setup/confirm", "", map[string]any{"mfaToken": mfaToken, "code": code})
	require.Equal(t, 200, res.StatusCode)
	done := decodeJSON(t, res)["data"].(map[string]any)
	assert.NotEmpty(t, done["token"], "finishing setup finishes the sign-in")
	assert.Len(t, done["recoveryCodes"], 10)
}

// Roadmap 115 M4: every sign-in is its own session; ending one ends only its tokens, and logout
// ends the session on the server instead of just dropping the cookie.
func TestEachSignInIsASessionAndLogoutEndsIt(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	email := uuid.NewString() + "@example.com"
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: email, Password: "first-password"})
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.sessions WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE identity_id = $1", m.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE id = $1", m.IdentityID)
	})
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	login := func() string {
		res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "first-password"})
		require.Equal(t, 200, res.StatusCode)
		return decodeJSON(t, res)["data"].(map[string]any)["token"].(string)
	}
	a, b := login(), login()

	live, err := identity.ListSessions(ctx, pool, m.IdentityID, time.Now())
	require.NoError(t, err)
	require.Len(t, live, 2)

	for _, tok := range []string{a, b} {
		assert.Equal(t, 200, doRequest(t, "GET", base+"/auth/me", tok, nil).StatusCode)
	}

	// Ending one session at the server ends that token and not the other.
	require.NoError(t, identity.RevokeSession(ctx, pool, m.IdentityID, live[0].ID, time.Now()))
	statuses := []int{doRequest(t, "GET", base+"/auth/me", a, nil).StatusCode, doRequest(t, "GET", base+"/auth/me", b, nil).StatusCode}
	assert.ElementsMatch(t, []int{200, 401}, statuses)

	// Logging out with the surviving token ends its session too: a copied token is dead.
	survivor := a
	if statuses[0] == 401 {
		survivor = b
	}
	res := doRequest(t, "POST", base+"/auth/logout", survivor, nil)
	assert.Equal(t, 204, res.StatusCode)
	assert.Equal(t, 401, doRequest(t, "GET", base+"/auth/me", survivor, nil).StatusCode)
	live, err = identity.ListSessions(ctx, pool, m.IdentityID, time.Now())
	require.NoError(t, err)
	assert.Empty(t, live)
}
