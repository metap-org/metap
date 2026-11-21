//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/captcha"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
)

// Roadmap 115 M2: signup is off by default, validates what is typed, answers the same whatever
// the address, and only a verified link creates the tenant — with the person as its admin.
func TestSelfServiceSignup(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	email := uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id IN (SELECT id FROM metadata.users WHERE lower(email) = lower($1))", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = $1", config.SignupEnabled)
	})
	form := map[string]any{"email": email, "password": "long-enough-pw", "tenantName": "Acme Labs"}

	// Off by default, and a sign-in page can ask.
	res := doRequest(t, "GET", base+"/auth/signup/status", "", nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, false, decodeJSON(t, res)["data"].(map[string]any)["enabled"])
	res = doRequest(t, "POST", base+"/auth/signup", "", form)
	require.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "signup_disabled", decodeJSON(t, res)["error"].(map[string]any)["code"])

	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupEnabled, json.RawMessage("true")))
	res = doRequest(t, "GET", base+"/auth/signup/status", "", nil)
	assert.Equal(t, true, decodeJSON(t, res)["data"].(map[string]any)["enabled"])

	// Validation is reported (it tells nothing about who has an account).
	res = doRequest(t, "POST", base+"/auth/signup", "", map[string]any{"email": "nope", "password": "x", "tenantName": ""})
	require.Equal(t, 400, res.StatusCode)
	fe := decodeJSON(t, res)["error"].(map[string]any)["fieldErrors"].(map[string]any)
	assert.Contains(t, fe, "email")
	assert.Contains(t, fe, "password")
	assert.Contains(t, fe, "tenantName")

	res = doRequest(t, "POST", base+"/auth/signup", "", form)
	require.Equal(t, 202, res.StatusCode)

	// Nothing exists until the link is opened.
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.identities WHERE lower(email) = lower($1)", email).Scan(&n))
	assert.Zero(t, n)

	// A bad or expired token is refused.
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": "nonsense"})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "signup_invalid", decodeJSON(t, res)["error"].(map[string]any)["code"])
	old, err := identity.CreateSignup(ctx, pool, email, "Old", "", "long-enough-pw", time.Now().Add(-48*time.Hour))
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": old})
	assert.Equal(t, 400, res.StatusCode)

	// The real link creates the tenant, makes the person its admin, and signs them in.
	token, err := identity.CreateSignup(ctx, pool, email, "Acme Labs", "Ada", "long-enough-pw", time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.NotEmpty(t, token)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": token})
	require.Equal(t, 200, res.StatusCode)
	data := decodeJSON(t, res)["data"].(map[string]any)
	session := data["token"].(string)
	me := decodeJSON(t, doRequest(t, "GET", base+"/auth/me", session, nil))["data"].(map[string]any)
	assert.Equal(t, data["tenantId"], me["tenantId"])
	assert.Contains(t, me["roles"], "admin")
	var name, tier, status string
	require.NoError(t, pool.QueryRow(ctx, "SELECT name, tier, status FROM control.tenants WHERE id = $1", data["tenantId"]).Scan(&name, &tier, &status))
	assert.Equal(t, []string{"Acme Labs", "trial", "active"}, []string{name, tier, status})
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", data["tenantId"]) })

	// The password chosen at signup works at the normal login.
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "long-enough-pw"})
	require.Equal(t, 200, res.StatusCode)

	// The link is single use.
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": token})
	assert.Equal(t, 400, res.StatusCode)

	// An address that already has an account keeps its password and gets a second tenant.
	again, err := identity.CreateSignup(ctx, pool, email, "Second Co", "", "a-different-password", time.Now().Add(-30*time.Minute))
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": again})
	require.Equal(t, 200, res.StatusCode)
	second := decodeJSON(t, res)["data"].(map[string]any)["tenantId"]
	assert.NotEqual(t, data["tenantId"], second)
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", second) })
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "a-different-password"})
	assert.Equal(t, 401, res.StatusCode, "the password was not replaced")

	// Hourly limit per address: the 4th request inside the hour yields no link, silently.
	spam := uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", spam)
	})
	for i := 0; i < identity.MaxSignupsPerEmailHr; i++ {
		tok, err := identity.CreateSignup(ctx, pool, spam, "X", "", "long-enough-pw", time.Now())
		require.NoError(t, err)
		require.NotEmpty(t, tok)
	}
	tok, err := identity.CreateSignup(ctx, pool, spam, "X", "", "long-enough-pw", time.Now())
	require.NoError(t, err)
	assert.Empty(t, tok)
}

// Reserved and operator-listed names cannot be registered, however they are spelled, and a name
// added to the list after a link was sent stops that link too.
func TestSignupRefusesBlockedTenantNames(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key IN ($1, $2)", config.SignupEnabled, config.SignupBlockedTenantNames)
	})
	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupEnabled, json.RawMessage("true")))
	email := uuid.NewString() + "@example.com"
	attempt := func(name string) (int, map[string]any) {
		res := doRequest(t, "POST", base+"/auth/signup", "", map[string]any{"email": email, "password": "long-enough-pw", "tenantName": name})
		body := decodeJSON(t, res)
		return res.StatusCode, body
	}

	for _, name := range []string{"admin", "  ADMIN ", "A.d-M i N", "Support", "METAP"} {
		code, body := attempt(name)
		require.Equal(t, 400, code, name)
		fe := body["error"].(map[string]any)["fieldErrors"].(map[string]any)
		assert.Contains(t, fe, "tenantName", name)
	}
	code, _ := attempt("Acme Admin Labs")
	assert.Equal(t, 202, code, "only the whole name is compared, so ordinary names containing a reserved word pass")

	// The operator's own list.
	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupBlockedTenantNames, json.RawMessage(`["PayPal", "Big Bank"]`)))
	for _, name := range []string{"paypal", "Big-Bank!"} {
		code, _ := attempt(name)
		assert.Equal(t, 400, code, name)
	}
	assert.Error(t, state.Config.SetPlatformGlobal(ctx, config.SignupBlockedTenantNames, json.RawMessage(`[""]`)), "an empty entry is rejected")
	assert.Error(t, state.Config.SetPlatformGlobal(ctx, config.SignupBlockedTenantNames, json.RawMessage(`"x"`)))

	// A link sent before the name was blocked is refused, and is not used up.
	token, err := identity.CreateSignup(ctx, pool, email, "Later Blocked", "", "long-enough-pw", time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupBlockedTenantNames, json.RawMessage(`["later blocked"]`)))
	res := doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": token})
	assert.Equal(t, 400, res.StatusCode)
	var left int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.signups WHERE lower(email) = lower($1) AND tenant_name = 'Later Blocked'", email).Scan(&left))
	assert.Equal(t, 1, left, "the link survives a refused attempt")
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", email)
	})
}

// With a CAPTCHA provider configured, signup needs a token the provider accepts; an outage is a
// 503 rather than a "wrong answer".
func TestSignupRequiresCaptchaWhenConfigured(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	var seenIP string
	state.Captcha = captcha.VerifierFunc(func(_ context.Context, token, ip string) error {
		seenIP = ip
		switch token {
		case "good":
			return nil
		case "outage":
			return errors.New("provider unreachable")
		}
		return captcha.ErrFailed
	})
	base := startTestServer(t, state, nil)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = $1", config.SignupEnabled)
	})
	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupEnabled, json.RawMessage("true")))
	email := uuid.NewString() + "@example.com"
	post := func(token string) (int, map[string]any) {
		form := map[string]any{"email": email, "password": "long-enough-pw", "tenantName": "Captcha Co"}
		if token != "" {
			form["captchaToken"] = token
		}
		res := doRequest(t, "POST", base+"/auth/signup", "", form)
		return res.StatusCode, decodeJSON(t, res)
	}

	code, body := post("")
	require.Equal(t, 400, code)
	assert.Equal(t, "captcha_failed", body["error"].(map[string]any)["code"])
	code, _ = post("wrong")
	assert.Equal(t, 400, code)
	code, body = post("outage")
	require.Equal(t, 503, code)
	assert.Equal(t, "captcha_failed", body["error"].(map[string]any)["code"])
	code, _ = post("good")
	assert.Equal(t, 202, code)
	assert.NotEmpty(t, seenIP, "the caller's address is passed on to the provider")

	// Bad form data is rejected before the provider is asked (no wasted verification).
	seenIP = ""
	res := doRequest(t, "POST", base+"/auth/signup", "", map[string]any{"email": "nope", "password": "x", "tenantName": "", "captchaToken": "good"})
	assert.Equal(t, 400, res.StatusCode)
	assert.Empty(t, seenIP)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", email)
	})
}

// With debug logging on, the verification link and token are in the log, so signup can be tried
// without a mail server; at the default level they are not.
func TestSignupVerificationLinkIsLoggedOnlyAtDebug(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = $1", config.SignupEnabled)
	})
	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupEnabled, json.RawMessage("true")))

	run := func(level slog.Level) string {
		var buf syncBuffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
		defer slog.SetDefault(prev)
		email := uuid.NewString() + "@example.com"
		t.Cleanup(func() {
			pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", email)
		})
		res := doRequest(t, "POST", base+"/auth/signup", "", map[string]any{"email": email, "password": "long-enough-pw", "tenantName": "Logged Co"})
		require.Equal(t, 202, res.StatusCode)
		require.Eventually(t, func() bool {
			var n int
			_ = pool.QueryRow(ctx, "SELECT count(*) FROM metadata.signups WHERE lower(email) = lower($1)", email).Scan(&n)
			return n == 1
		}, 3*time.Second, 20*time.Millisecond)
		time.Sleep(100 * time.Millisecond) // the log line follows the insert
		return buf.String()
	}

	debug := run(slog.LevelDebug)
	require.Contains(t, debug, "signup verification link")
	m := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(debug)
	require.NotNil(t, m, debug)
	// The logged token is the real one: it opens the signup.
	res := doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": m[1]})
	require.Equal(t, 200, res.StatusCode)
	tenant := decodeJSON(t, res)["data"].(map[string]any)["tenantId"]
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", tenant) })

	assert.NotContains(t, run(slog.LevelInfo), "signup verification link")
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
