//go:build integration

package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discoveryOnly is an OIDC issuer that answers only discovery — all SetSsoConfig needs to prove
// the issuer is real.
func discoveryOnly(t *testing.T) string {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Roadmap 115 M7: a tenant admin connects an OIDC provider; the secret is write-only; enforcing
// SSO is the owner's call; bad input is refused.
func TestSsoAdminConfig(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	owner := h.member(tenant, "owner@acme.io", "admin")
	other := h.member(tenant, "admin2@acme.io", "admin")
	h.policyRole(tenant, "viewer")
	asOwner, asOther := h.as(tenant, owner.ID), h.as(tenant, other.ID)
	issuer := discoveryOnly(t)

	cfg, err := h.client.SSO.GetSsoConfig(asOwner, &Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.GetConfigured())
	assert.Equal(t, "http://portal.test/auth/sso/callback", cfg.GetRedirectUri(), "what to register at the IdP")

	valid := func() *SetSsoConfigRequest {
		return &SetSsoConfigRequest{IssuerUrl: issuer, ClientId: "c1", ClientSecret: "s3cret", EmailDomains: []string{"Acme.io"},
			DefaultRole: "viewer", Enabled: true, PostLoginRedirect: "http://portal.test/home"}
	}
	code := func(mut func(*SetSsoConfigRequest)) int {
		r := valid()
		mut(r)
		_, err := h.client.SSO.SetSsoConfig(asOwner, r)
		return statusOf(err)
	}
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.IssuerUrl = "http://idp.example.com" }), "https only")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.IssuerUrl = "https://idp.invalid" }), "must be a reachable OIDC issuer")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.ClientId = "" }))
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.PostLoginRedirect = "https://evil.example.com/" }), "landing page must be on the service's own origin")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.DefaultRole = "admin" }), "SSO never mints admins")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.DefaultRole = "no-such-role" }))
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.EmailDomains = []string{"not a domain"} }))
	// Group to role mapping: needs a claim, never admin, only roles the tenant has.
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.RoleMap = map[string]string{"g": "viewer"} }), "a map needs a claim to read")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) {
		r.RoleClaim, r.RoleMap = "groups", map[string]string{"g": "admin"}
	}), "a provider group never makes an admin")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) {
		r.RoleClaim, r.RoleMap = "groups", map[string]string{"g": "platform_admin"}
	}))
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) {
		r.RoleClaim, r.RoleMap = "groups", map[string]string{"g": "no-such-role"}
	}))
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) {
		r.RoleClaim, r.RoleMap = "groups", map[string]string{"": "viewer"}
	}))
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSsoConfigRequest) { r.ClientSecret = "" }), "the first setup needs a secret")
	_, err = h.client.SSO.SetSsoConfig(h.as(tenant, uuid.New()), valid())
	assert.Equal(t, http.StatusForbidden, statusOf(err), "admins only")

	set, err := h.client.SSO.SetSsoConfig(asOther, valid())
	require.NoError(t, err)
	assert.True(t, set.GetConfigured() && set.GetEnabled() && set.GetHasClientSecret())
	assert.Equal(t, []string{"acme.io"}, set.GetEmailDomains(), "domains are normalised")
	assert.Equal(t, []string{"openid", "email", "profile"}, set.GetScopes())

	// A valid mapping is stored and read back (never null).
	mapped := valid()
	mapped.RoleClaim, mapped.RoleMap = " groups ", map[string]string{"sec-team": "viewer"}
	set, err = h.client.SSO.SetSsoConfig(asOther, mapped)
	require.NoError(t, err)
	assert.Equal(t, "groups", set.RoleClaim)
	assert.Equal(t, map[string]string{"sec-team": "viewer"}, set.RoleMap)
	plain, err := h.client.SSO.SetSsoConfig(asOther, valid())
	require.NoError(t, err)
	assert.NotNil(t, plain.RoleMap, "no mapping reads back as {}, not null")
	assert.Empty(t, plain.RoleMap)

	// A later update without a secret keeps the stored one.
	upd := valid()
	upd.ClientSecret, upd.ClientId = "", "c2"
	set, err = h.client.SSO.SetSsoConfig(asOther, upd)
	require.NoError(t, err)
	assert.Equal(t, "c2", set.GetClientId())
	assert.True(t, set.GetHasClientSecret())

	// Only the owner can enforce SSO, and only while it is enabled.
	enforce := valid()
	enforce.ClientSecret, enforce.Enforced = "", true
	_, err = h.client.SSO.SetSsoConfig(asOther, enforce)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	disabledEnforce := valid()
	disabledEnforce.ClientSecret, disabledEnforce.Enforced, disabledEnforce.Enabled = "", true, false
	_, err = h.client.SSO.SetSsoConfig(asOwner, disabledEnforce)
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	set, err = h.client.SSO.SetSsoConfig(asOwner, enforce)
	require.NoError(t, err)
	assert.True(t, set.GetEnforced())

	// Removing it restores password sign-in and drops the secret.
	_, err = h.client.SSO.DeleteSsoConfig(asOther, &Empty{})
	require.NoError(t, err)
	cfg, err = h.client.SSO.GetSsoConfig(asOwner, &Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.GetConfigured())
	trail, err := h.client.Audit.SearchAudit(asOwner, &SearchAuditRequest{Entity: AuditEntitySSO})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(trail.GetEntries()), 3, "every change is audited")
}

// The domains a workspace names start pending with a token to publish; a check that finds the
// record verifies them; one other workspace cannot then take the same domain.
func TestSsoDomainOwnershipIsProvedByDNS(t *testing.T) {
	h := newIdentityHarness(t)
	issuer := discoveryOnly(t)
	records := map[string][]string{}
	withLookup := h.cfg
	withLookup.TXTLookup = func(_ context.Context, name string) ([]string, error) {
		if v, ok := records[name]; ok {
			return v, nil
		}
		return nil, errors.New("no such host")
	}
	h.client.SSO = NewSsoAdminService(withLookup)
	setup := func(tenant uuid.UUID, email string) context.Context {
		admin := h.member(tenant, email, "admin")
		return h.as(tenant, admin.ID)
	}
	save := func(ctx context.Context, domains ...string) *SsoConfig {
		cfg, err := h.client.SSO.SetSsoConfig(ctx, &SetSsoConfigRequest{IssuerUrl: issuer, ClientId: "c1", ClientSecret: "s3cret",
			EmailDomains: domains, Enabled: true, PostLoginRedirect: "http://portal.test/home"})
		require.NoError(t, err)
		return cfg
	}
	byName := func(cfg *SsoConfig, domain string) *SsoDomain {
		for _, d := range cfg.Domains {
			if d.Domain == domain {
				return d
			}
		}
		return nil
	}

	acme, rival := uuid.New(), uuid.New()
	asAcme, asRival := setup(acme, "admin@acme.io"), setup(rival, "admin@rival.io")

	cfg := save(asAcme, "acme.io", "acme.org")
	d := byName(cfg, "acme.io")
	require.NotNil(t, d)
	assert.Equal(t, "pending", d.Status)
	assert.Equal(t, "_metap-verify.acme.io", d.VerifyHost)
	assert.NotEmpty(t, d.VerifyValue)

	// No record yet, and a record with the wrong value, both leave it pending.
	_, err := h.client.SSO.VerifySsoDomain(asAcme, &VerifySsoDomainRequest{Domain: "acme.io"})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	records["_metap-verify.acme.io"] = []string{"something-else"}
	_, err = h.client.SSO.VerifySsoDomain(asAcme, &VerifySsoDomainRequest{Domain: "acme.io"})
	assert.Equal(t, http.StatusConflict, statusOf(err))

	records["_metap-verify.acme.io"] = []string{"unrelated", d.VerifyValue}
	cfg, err = h.client.SSO.VerifySsoDomain(asAcme, &VerifySsoDomainRequest{Domain: "acme.io"})
	require.NoError(t, err)
	assert.Equal(t, "verified", byName(cfg, "acme.io").Status)
	assert.Equal(t, "pending", byName(cfg, "acme.org").Status, "each domain is proved on its own")

	// Re-saving the settings keeps a verified domain verified (and its token).
	cfg = save(asAcme, "acme.io", "acme.org")
	assert.Equal(t, "verified", byName(cfg, "acme.io").Status)
	assert.Equal(t, d.VerifyValue, byName(cfg, "acme.io").VerifyValue)

	// A domain the workspace did not name cannot be checked.
	_, err = h.client.SSO.VerifySsoDomain(asAcme, &VerifySsoDomainRequest{Domain: "unnamed.io"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))

	// Another workspace can name it, but cannot verify what acme already owns.
	rcfg := save(asRival, "acme.io")
	records["_metap-verify.acme.io"] = []string{byName(rcfg, "acme.io").VerifyValue}
	_, err = h.client.SSO.VerifySsoDomain(asRival, &VerifySsoDomainRequest{Domain: "acme.io"})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	rcfg, err = h.client.SSO.GetSsoConfig(asRival, &Empty{})
	require.NoError(t, err)
	assert.Equal(t, "pending", byName(rcfg, "acme.io").Status)

	// Dropping a domain from the settings drops its row.
	cfg = save(asAcme, "acme.io")
	assert.Nil(t, byName(cfg, "acme.org"))
}

// samlMetadataXML is metadata for a throwaway SAML identity provider.
func samlMetadataXML(t *testing.T) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	meta, _ := url.Parse("https://idp.example.com/metadata")
	sso, _ := url.Parse("https://idp.example.com/sso")
	idp := &saml.IdentityProvider{Key: key, Signer: key, Certificate: cert, MetadataURL: *meta, SSOURL: *sso}
	raw, err := xml.Marshal(idp.Metadata())
	require.NoError(t, err)
	return string(raw)
}

// SAML settings: the provider's metadata is checked when saved (as XML or fetched from a URL), the
// endpoints to register at the provider are returned, the shared rules apply (never admin, only the
// owner enforces), and only one protocol can be on.
func TestSamlAdminConfig(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	owner := h.member(tenant, "owner@acme.io", "admin")
	other := h.member(tenant, "admin2@acme.io", "admin")
	h.policyRole(tenant, "viewer")
	asOwner, asOther := h.as(tenant, owner.ID), h.as(tenant, other.ID)
	metadata := samlMetadataXML(t)

	cfg, err := h.client.SSO.GetSamlConfig(asOwner, &Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.Configured)
	assert.Equal(t, "http://portal.test/auth/saml/"+tenant.String()+"/acs", cfg.AcsUrl, "what to register at the provider")
	assert.Equal(t, "http://portal.test/auth/saml/"+tenant.String()+"/metadata", cfg.EntityId)

	valid := func() *SetSamlConfigRequest {
		return &SetSamlConfigRequest{IdpMetadataXml: metadata, EmailDomains: []string{"Acme.io"}, DefaultRole: "viewer", Enabled: true,
			PostLoginRedirect: "http://portal.test/home"}
	}
	code := func(mut func(*SetSamlConfigRequest)) int {
		r := valid()
		mut(r)
		_, err := h.client.SSO.SetSamlConfig(asOwner, r)
		return statusOf(err)
	}
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.IdpMetadataXml = "<nope/>" }), "must be SAML metadata")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.IdpMetadataXml = "" }), "the first setup needs metadata")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.IdpMetadataXml, r.IdpMetadataUrl = "", "http://idp.example.com/m" }), "https only")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.DefaultRole = "admin" }), "SSO never mints admins")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.RoleMap = map[string]string{"g": "viewer"} }), "a map needs an attribute")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) {
		r.RoleAttribute, r.RoleMap = "groups", map[string]string{"g": "admin"}
	}), "a provider group never makes an admin")
	assert.Equal(t, http.StatusBadRequest, code(func(r *SetSamlConfigRequest) { r.PostLoginRedirect = "https://evil.example.com/" }))
	_, err = h.client.SSO.SetSamlConfig(h.as(tenant, uuid.New()), valid())
	assert.Equal(t, http.StatusForbidden, statusOf(err), "admins only")

	// Metadata fetched from a URL is stored.
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(metadata)) }))
	t.Cleanup(served.Close)
	byURL := valid()
	byURL.IdpMetadataXml, byURL.IdpMetadataUrl = "", served.URL
	set, err := h.client.SSO.SetSamlConfig(asOther, byURL)
	require.NoError(t, err, "plain http is accepted for a loopback provider")
	assert.True(t, set.Configured && set.Enabled)
	assert.Equal(t, "https://idp.example.com/metadata", set.IdpEntityId)
	assert.Equal(t, "https://idp.example.com/sso", set.IdpSsoUrl)
	assert.Equal(t, []string{"acme.io"}, set.EmailDomains)
	require.Len(t, set.Domains, 1)
	assert.Equal(t, "pending", set.Domains[0].Status, "domains start unproved")

	// A later save without metadata keeps what is stored.
	keep := valid()
	keep.IdpMetadataXml, keep.DefaultRole = "", ""
	set, err = h.client.SSO.SetSamlConfig(asOther, keep)
	require.NoError(t, err)
	assert.Equal(t, "https://idp.example.com/metadata", set.IdpEntityId)

	// Only the owner enforces.
	enforce := valid()
	enforce.IdpMetadataXml, enforce.Enforced = "", true
	_, err = h.client.SSO.SetSamlConfig(asOther, enforce)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	set, err = h.client.SSO.SetSamlConfig(asOwner, enforce)
	require.NoError(t, err)
	assert.True(t, set.Enforced)

	// One protocol at a time.
	oidc := &SetSsoConfigRequest{IssuerUrl: discoveryOnly(t), ClientId: "c1", ClientSecret: "s3cret", Enabled: true}
	_, err = h.client.SSO.SetSsoConfig(asOwner, oidc)
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "SAML is on")

	_, err = h.client.SSO.DeleteSamlConfig(asOther, &Empty{})
	require.NoError(t, err)
	cfg, err = h.client.SSO.GetSamlConfig(asOwner, &Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.Configured)
	_, err = h.client.SSO.SetSsoConfig(asOwner, oidc)
	assert.NoError(t, err, "with SAML removed, OpenID Connect can be turned on")
	_, err = h.client.SSO.SetSamlConfig(asOwner, valid())
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "and now OpenID Connect is on")
}
