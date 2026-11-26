package ssoconfig

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/crud"
)

func TestSameOrigin(t *testing.T) {
	assert.True(t, sameOrigin("https://app.acme.io/home", "https://APP.acme.io"))
	assert.False(t, sameOrigin("https://evil.io/", "https://app.acme.io"))
	assert.False(t, sameOrigin("http://app.acme.io/", "https://app.acme.io"), "scheme must match")
	assert.False(t, sameOrigin("javascript:alert(1)", "https://app.acme.io"))
	assert.False(t, sameOrigin("/relative", "https://app.acme.io"))
}

func TestCheckIssuer(t *testing.T) {
	assert.NoError(t, checkIssuer("https://idp.acme.io"))
	assert.NoError(t, checkIssuer("http://localhost:8080"), "loopback dev providers")
	assert.Error(t, checkIssuer("http://idp.acme.io"))
	assert.Error(t, checkIssuer("ftp://idp.acme.io"))
	assert.Error(t, checkIssuer("not a url"))
}

func TestCallbackURL(t *testing.T) {
	assert.Equal(t, "https://app.acme.io/auth/sso/callback", callbackURL("https://app.acme.io/"))
	assert.Empty(t, callbackURL(""))
}

func status(t *testing.T, err error) int {
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	return int(se.Status)
}

func TestCheckAccess(t *testing.T) {
	base := "https://app.acme.io"
	a, err := checkAccess(accessInput{EmailDomains: []string{" Acme.IO "}, DefaultRole: "viewer", RoleClaim: "groups",
		RoleMap: map[string]string{"soc": "analyst"}, PostLoginRedirect: base + "/home", Enabled: true}, base)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme.io"}, a.domains)
	assert.ElementsMatch(t, []string{"viewer", "analyst"}, a.roles())

	for name, in := range map[string]accessInput{
		"bad domain":        {EmailDomains: []string{"not a domain"}},
		"foreign landing":   {PostLoginRedirect: "https://evil.io/"},
		"admin default":     {DefaultRole: "admin"},
		"blank map line":    {RoleClaim: "g", RoleMap: map[string]string{"x": " "}},
		"map without claim": {RoleMap: map[string]string{"x": "viewer"}},
		"admin mapped":      {RoleClaim: "g", RoleMap: map[string]string{"x": "admin"}},
		"platform mapped":   {RoleClaim: "g", RoleMap: map[string]string{"x": "platform_admin"}},
		"enforce off":       {Enforced: true},
	} {
		_, err := checkAccess(in, base)
		require.Error(t, err, name)
		assert.Equal(t, http.StatusBadRequest, status(t, err), name)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = strings.Repeat("a", i+1) + ".io"
	}
	_, err = checkAccess(accessInput{EmailDomains: many}, base)
	assert.Error(t, err, "at most 20 domains")
}

func TestCheckNameID(t *testing.T) {
	assert.NoError(t, checkNameID(sso.NameIDEmail, ""))
	assert.NoError(t, checkNameID(sso.NameIDPersistent, "mail"))
	assert.Error(t, checkNameID(sso.NameIDPersistent, " "), "persistent carries the address elsewhere")
	assert.Error(t, checkNameID("transient", ""))
}

func TestViews(t *testing.T) {
	assert.Equal(t, &SsoConfig{RedirectUri: "r"}, oidcView("r", nil, false, false, nil))
	v := oidcView("r", &sso.OidcConfig{IssuerURL: "https://i"}, true, true, []sso.SSODomain{{Domain: "acme.io", Token: "t"}})
	assert.True(t, v.Configured)
	assert.True(t, v.HasClientSecret)
	assert.Equal(t, map[string]string{}, v.RoleMap)
	require.Len(t, v.Domains, 1)
	assert.Equal(t, "pending", v.Domains[0].Status)

	s := samlView("https://app.acme.io", uuid.New(), nil, false, nil)
	assert.False(t, s.Configured)
	assert.NotEmpty(t, s.AcsUrl)
	s = samlView("https://app.acme.io", uuid.New(), &sso.SamlConfig{}, true, nil)
	assert.Equal(t, sso.NameIDEmail, s.NameIdFormat, "email is the default format")
}
