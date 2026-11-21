//go:build integration

package httpserver_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// testSAMLIdP is a real SAML identity provider (crewjam's), signing real assertions with its own
// key, so the service provider side is exercised end to end: AuthnRequest out, signed response back.
type testSAMLIdP struct {
	idp     *saml.IdentityProvider
	srv     *httptest.Server
	session *saml.Session
	// spMetadataURL is where the provider reads the service provider's metadata from.
	spMetadataURL string
}

func newSAMLKey(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return key, cert
}

// startSAMLIdP serves an identity provider. When entityBase is set the provider reuses that
// provider's entity ID and sign-in URL (but not its key), to model a forger.
func startSAMLIdP(t *testing.T, entityBase string) *testSAMLIdP {
	p := &testSAMLIdP{}
	key, cert := newSAMLKey(t)
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	base, _ := url.Parse(p.srv.URL)
	meta := *base
	meta.Path = "/metadata"
	if entityBase != "" {
		u, _ := url.Parse(entityBase)
		meta = *u
	}
	sso := *base
	sso.Path = "/sso"
	if entityBase != "" {
		u, _ := url.Parse(entityBase)
		sso = *u
		sso.Path = "/sso"
	}
	p.idp = &saml.IdentityProvider{Key: key, Signer: key, Certificate: cert, MetadataURL: meta, SSOURL: sso,
		ServiceProviderProvider: spProvider{p}, SessionProvider: sessionFunc(func() *saml.Session { return p.session })}
	mux.HandleFunc("/metadata", p.idp.ServeMetadata)
	mux.HandleFunc("/sso", p.idp.ServeSSO)
	return p
}

type spProvider struct{ p *testSAMLIdP }

func (s spProvider) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	res, err := http.Get(s.p.spMetadataURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(raw, &ed); err != nil {
		return nil, os.ErrNotExist
	}
	return &ed, nil
}

type sessionFunc func() *saml.Session

func (f sessionFunc) GetSession(http.ResponseWriter, *http.Request, *saml.IdpAuthnRequest) *saml.Session {
	return f()
}

// signIn sets who the provider will sign in next.
func (p *testSAMLIdP) signIn(email string, groups ...string) {
	attrs := []saml.Attribute{}
	if len(groups) > 0 {
		var values []saml.AttributeValue
		for _, g := range groups {
			values = append(values, saml.AttributeValue{Type: "xs:string", Value: g})
		}
		attrs = append(attrs, saml.Attribute{Name: "groups", NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic", Values: values})
	}
	p.session = &saml.Session{ID: uuid.NewString(), CreateTime: time.Now(), ExpireTime: time.Now().Add(time.Hour), Index: "1",
		NameID: email, NameIDFormat: string(saml.EmailAddressNameIDFormat), UserEmail: email, CustomAttributes: attrs}
}

func (p *testSAMLIdP) metadataXML(t *testing.T) string {
	raw, err := xml.Marshal(p.idp.Metadata())
	require.NoError(t, err)
	return string(raw)
}

var (
	samlResponseField = regexp.MustCompile(`name="SAMLResponse" value="([^"]*)"`)
	relayStateField   = regexp.MustCompile(`name="RelayState" value="([^"]*)"`)
)

// samlRound walks the browser flow up to the provider's answer: start on our service, the
// provider's sign-in page, and the auto-post form it returns. It hands back what the browser
// would post to the ACS.
func samlRound(t *testing.T, base string, tenant uuid.UUID, idp *testSAMLIdP) (acsURL string, form url.Values) {
	t.Helper()
	start := get(t, base+"/auth/saml/"+tenant.String()+"/start")
	require.Equal(t, 302, start.StatusCode)
	start.Body.Close()
	// The browser goes where the redirect says; a forger intercepts by answering at the same path.
	loc, err := url.Parse(start.Header.Get("Location"))
	require.NoError(t, err)
	via, _ := url.Parse(idp.srv.URL)
	loc.Scheme, loc.Host = via.Scheme, via.Host
	page := get(t, loc.String())
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	require.Equal(t, 200, page.StatusCode, "the provider answered: %s", body)
	resp := samlResponseField.FindSubmatch(body)
	relay := relayStateField.FindSubmatch(body)
	require.NotNil(t, resp, "the provider's page carries a SAMLResponse: %s", body)
	form = url.Values{"SAMLResponse": {html.UnescapeString(string(resp[1]))}}
	if relay != nil {
		form.Set("RelayState", html.UnescapeString(string(relay[1])))
	}
	return base + "/auth/saml/" + tenant.String() + "/acs", form
}

func postACS(t *testing.T, acsURL string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", acsURL, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := noFollow.Do(req)
	require.NoError(t, err)
	return res
}

// setupSAMLTenant registers a tenant against idp (which learns the service provider's metadata URL).
func setupSAMLTenant(t *testing.T, idp *testSAMLIdP, mutate func(*tenantauth.SamlConfig)) (base string, tenant uuid.UUID, cfg *tenantauth.SamlConfig, pool identity.DBTX) {
	t.Helper()
	p := httpTestPool(t)
	tenant = uuid.New()
	state, _ := newTestAppState(t, p, httpTestEntity())
	base = startTestServerWithPublicURL(t, state)
	idp.spMetadataURL = base + "/auth/saml/" + tenant.String() + "/metadata"
	cfg = &tenantauth.SamlConfig{IdpMetadataXML: idp.metadataXML(t), EmailDomains: []string{"acme.io"}, PostLoginRedirect: base + "/home"}
	if mutate != nil {
		mutate(cfg)
	}
	require.NoError(t, tenantauth.SaveSamlConfig(context.Background(), p, tenant, cfg, true))
	verifyTestDomain(t, p, tenant, "acme.io")
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DELETE FROM metadata.tenant_auth_configs WHERE tenant_id = $1`, tenant)
	})
	return base, tenant, cfg, p
}

func sessionCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if strings.Contains(c.Name, "session") && !strings.Contains(c.Name, "started") {
			return c
		}
	}
	return nil
}

// A signed assertion from the tenant's provider signs the person in: provisioned on first sight
// with the default role plus the roles their groups map to, found by subject afterwards.
func TestSAMLSignInProvisionsAndMapsRoles(t *testing.T) {
	idp := startSAMLIdP(t, "")
	base, tenant, _, pool := setupSAMLTenant(t, idp, func(c *tenantauth.SamlConfig) {
		c.DefaultRole = "viewer"
		c.RoleAttribute = "groups"
		c.RoleMap = map[string]string{"sec-team": "security_analyst"}
	})
	ctx := context.Background()

	// The metadata an admin registers at the provider.
	res := get(t, base+"/auth/saml/"+tenant.String()+"/metadata")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	assert.Contains(t, string(raw), base+"/auth/saml/"+tenant.String()+"/acs")

	idp.signIn("dana@acme.io", "sec-team", "unrelated")
	acs, form := samlRound(t, base, tenant, idp)
	cb := postACS(t, acs, form)
	if cb.StatusCode != 302 {
		b, _ := io.ReadAll(cb.Body)
		t.Fatalf("acs: %d %s", cb.StatusCode, b)
	}
	cb.Body.Close()
	assert.Equal(t, base+"/home", cb.Header.Get("Location"))
	require.NotNil(t, sessionCookie(cb), "a session cookie is set")

	dana, err := identity.FindExternalMember(ctx, pool, tenant, "saml", "dana@acme.io")
	require.NoError(t, err)
	require.NotNil(t, dana)
	assert.Equal(t, []string{"security_analyst", "viewer"}, rolesOf(t, pool, tenant, dana.ID))

	// Same person again: the same member; the group is gone, so is the mapped role.
	idp.signIn("dana@acme.io")
	acs, form = samlRound(t, base, tenant, idp)
	cb = postACS(t, acs, form)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
	assert.Equal(t, []string{"viewer"}, rolesOf(t, pool, tenant, dana.ID))

	// An address outside the allowed domains is refused.
	idp.signIn("intruder@evil.test")
	acs, form = samlRound(t, base, tenant, idp)
	cb = postACS(t, acs, form)
	assert.Equal(t, 403, cb.StatusCode)
	cb.Body.Close()
}

// The response must be signed by the tenant's provider, answer a request this service made, and
// be used once.
func TestSAMLRefusesForgedReplayedAndUnsolicitedResponses(t *testing.T) {
	idp := startSAMLIdP(t, "")
	base, tenant, _, _ := setupSAMLTenant(t, idp, nil)

	// A forger with the provider's entity ID but another key: the signature does not verify.
	forger := startSAMLIdP(t, idp.srv.URL+"/metadata")
	forger.spMetadataURL = idp.spMetadataURL
	forger.signIn("boss@acme.io")
	acs, form := samlRound(t, base, tenant, forger)
	res := postACS(t, acs, form)
	assert.Equal(t, 403, res.StatusCode, "an assertion signed by another key is refused")
	res.Body.Close()

	// A genuine response is used once.
	idp.signIn("dana@acme.io")
	acs, form = samlRound(t, base, tenant, idp)
	first := postACS(t, acs, form)
	require.Equal(t, 302, first.StatusCode)
	first.Body.Close()
	replay := postACS(t, acs, form)
	assert.Equal(t, 400, replay.StatusCode, "a used sign-in cannot be replayed")
	replay.Body.Close()

	// A genuine response carried under another sign-in's relay state does not answer that request.
	idp.signIn("dana@acme.io")
	acs, a := samlRound(t, base, tenant, idp)
	_, b := samlRound(t, base, tenant, idp)
	mixed := url.Values{"SAMLResponse": {a.Get("SAMLResponse")}, "RelayState": {b.Get("RelayState")}}
	res = postACS(t, acs, mixed)
	assert.Equal(t, 403, res.StatusCode, "InResponseTo must match the request behind the relay state")
	res.Body.Close()

	// No relay state at all: unsolicited (provider-initiated) responses are not accepted.
	res = postACS(t, acs, url.Values{"SAMLResponse": {a.Get("SAMLResponse")}})
	assert.Equal(t, 400, res.StatusCode)
	res.Body.Close()
}

// A workspace that has not proved its domain gets no members created through SAML, and discovery
// routes to the SAML start URL once it has.
func TestSAMLDomainProofAndDiscoveryAndEnforcement(t *testing.T) {
	idp := startSAMLIdP(t, "")
	base, tenant, cfg, pool := setupSAMLTenant(t, idp, nil)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `UPDATE metadata.sso_domains SET verified_at = NULL WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)
	res := doRequest(t, "GET", base+"/auth/sso/discover?email=x@acme.io", "", nil)
	assert.Equal(t, false, decodeJSON(t, res)["data"].(map[string]any)["ssoAvailable"])
	idp.signIn("newbie@acme.io")
	acs, form := samlRound(t, base, tenant, idp)
	cb := postACS(t, acs, form)
	assert.Equal(t, 403, cb.StatusCode, "no member is created under an unproved domain")
	cb.Body.Close()

	ok, err := tenantauth.MarkDomainVerified(ctx, pool, tenant, "acme.io", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	res = doRequest(t, "GET", base+"/auth/sso/discover?email=x@acme.io", "", nil)
	data := decodeJSON(t, res)["data"].(map[string]any)
	assert.Equal(t, true, data["ssoAvailable"])
	assert.Equal(t, "saml", data["protocol"])
	assert.Equal(t, "/auth/saml/"+tenant.String()+"/start", data["startUrl"])

	// Enforced: passwords stop working for everyone but the owner.
	owner, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "owner@acme.io", Password: "Owner-Passw0rd!"})
	require.NoError(t, err)
	require.NoError(t, identity.SetOwner(ctx, pool, tenant, owner.ID))
	other, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "member@acme.io", Password: "Member-Passw0rd!"})
	require.NoError(t, err)
	_ = other
	cfg.Enforced = true
	require.NoError(t, tenantauth.SaveSamlConfig(ctx, pool, tenant, cfg, true))
	login := func(email, password string) *http.Response {
		return doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": password, "tenantId": tenant.String()})
	}
	res = login("member@acme.io", "Member-Passw0rd!")
	assert.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "sso_required", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res = login("owner@acme.io", "Owner-Passw0rd!")
	assert.Equal(t, 200, res.StatusCode, "the owner keeps password sign-in")
	res.Body.Close()

	// SAML still works when enforced, and links the existing member by their (proved) address.
	idp.signIn("member@acme.io")
	acs, form = samlRound(t, base, tenant, idp)
	cb = postACS(t, acs, form)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
	linked, err := identity.FindExternalMember(ctx, pool, tenant, "saml", "member@acme.io")
	require.NoError(t, err)
	require.NotNil(t, linked)
	assert.Equal(t, other.ID, linked.ID)
}
