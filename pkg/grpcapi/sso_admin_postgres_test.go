//go:build integration

package grpcapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/grpcapi/pb"
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

	cfg, err := h.client.SSO.GetSsoConfig(asOwner, &pb.Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.GetConfigured())
	assert.Equal(t, "http://portal.test/auth/sso/callback", cfg.GetRedirectUri(), "what to register at the IdP")

	valid := func() *pb.SetSsoConfigRequest {
		return &pb.SetSsoConfigRequest{IssuerUrl: issuer, ClientId: "c1", ClientSecret: "s3cret", EmailDomains: []string{"Acme.io"},
			DefaultRole: "viewer", Enabled: true, PostLoginRedirect: "http://portal.test/home"}
	}
	code := func(mut func(*pb.SetSsoConfigRequest)) codes.Code {
		r := valid()
		mut(r)
		_, err := h.client.SSO.SetSsoConfig(asOwner, r)
		return status.Code(err)
	}
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.IssuerUrl = "http://idp.example.com" }), "https only")
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.IssuerUrl = "https://idp.invalid" }), "must be a reachable OIDC issuer")
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.ClientId = "" }))
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.PostLoginRedirect = "https://evil.example.com/" }), "landing page must be on the service's own origin")
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.DefaultRole = "admin" }), "SSO never mints admins")
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.DefaultRole = "no-such-role" }))
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.EmailDomains = []string{"not a domain"} }))
	assert.Equal(t, codes.InvalidArgument, code(func(r *pb.SetSsoConfigRequest) { r.ClientSecret = "" }), "the first setup needs a secret")
	_, err = h.client.SSO.SetSsoConfig(h.as(tenant, uuid.New()), valid())
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "admins only")

	set, err := h.client.SSO.SetSsoConfig(asOther, valid())
	require.NoError(t, err)
	assert.True(t, set.GetConfigured() && set.GetEnabled() && set.GetHasClientSecret())
	assert.Equal(t, []string{"acme.io"}, set.GetEmailDomains(), "domains are normalised")
	assert.Equal(t, []string{"openid", "email", "profile"}, set.GetScopes())

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
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	disabledEnforce := valid()
	disabledEnforce.ClientSecret, disabledEnforce.Enforced, disabledEnforce.Enabled = "", true, false
	_, err = h.client.SSO.SetSsoConfig(asOwner, disabledEnforce)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	set, err = h.client.SSO.SetSsoConfig(asOwner, enforce)
	require.NoError(t, err)
	assert.True(t, set.GetEnforced())

	// Removing it restores password sign-in and drops the secret.
	_, err = h.client.SSO.DeleteSsoConfig(asOther, &pb.Empty{})
	require.NoError(t, err)
	cfg, err = h.client.SSO.GetSsoConfig(asOwner, &pb.Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.GetConfigured())
	trail, err := h.client.Audit.SearchAudit(asOwner, &pb.SearchAuditRequest{Entity: AuditEntitySSO})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(trail.GetEntries()), 3, "every change is audited")
}
