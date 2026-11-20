// Single sign-on over OIDC (roadmap 115 M7). A tenant admin configures the IdP through
// SsoAdminService; people then sign in through these public REST routes (a browser redirect flow
// has no GraphQL shape):
//
//	GET /auth/sso/discover?email=   which sign-in route this address should use
//	GET /auth/sso/start?tenant=     redirect to the tenant's IdP
//	GET /auth/sso/callback          the IdP sends the browser back here; a session starts
//
// MFA is not asked again after SSO: the IdP is the second factor (as for the other external
// providers). A tenant that enforces SSO refuses password sign-in for everyone but its owner.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// SSOSecretKey is the config key a tenant's OIDC client secret is stored under (control.TenantSecretRef).
const SSOSecretKey = "sso.clientSecret"

const ssoProvider = "oidc"

// ssoRequired refuses a password sign-in (or tenant switch) into a tenant that enforces SSO, unless
// the member is the tenant's owner — the way back in when the IdP is broken.
func ssoRequired(w http.ResponseWriter, r *http.Request, state *AppState, m identity.Membership) bool {
	enforced, err := tenantauth.SSOEnforced(r.Context(), state.Pool, m.TenantID)
	if err != nil {
		writeInternalError(w, err)
		return true
	}
	if !enforced {
		return false
	}
	owner, err := identity.OwnerID(r.Context(), state.Pool, m.TenantID)
	if err != nil {
		writeInternalError(w, err)
		return true
	}
	if owner == m.MemberID {
		return false
	}
	writeServiceError(w, http.StatusForbidden, errcode.SSORequired,
		"This workspace signs in through its identity provider.", nil)
	return true
}

// redirectTo sends the browser to target. Neither caller passes anything taken from the request:
// the sign-in URL comes from the tenant's configured identity provider and the landing page is
// checked to be on this service's own origin (SameOrigin) — which is why this sets the header
// itself rather than going through http.Redirect with the request.
func redirectTo(w http.ResponseWriter, target string) {
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

// SameOrigin reports whether raw is an absolute http(s) URL on the same scheme, host and port as base.
func SameOrigin(raw, base string) bool {
	u, err := url.Parse(raw)
	b, err2 := url.Parse(base)
	if err != nil || err2 != nil || u.Host == "" || b.Host == "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == b.Scheme && strings.EqualFold(u.Host, b.Host)
}

func ssoRedirectURI(state *AppState) string {
	return strings.TrimRight(state.PublicAppURL, "/") + "/auth/sso/callback"
}

func ssoClientSecret(ctx context.Context, state *AppState, cfg *tenantauth.OidcConfig) (string, error) {
	return state.Router.Secrets().GetSecret(ctx, cfg.ClientSecretRef)
}

func ssoDiscover(w http.ResponseWriter, r *http.Request, state *AppState) {
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`email` must be an email address.", nil)
		return
	}
	tenants, err := tenantauth.TenantsForEmailDomain(r.Context(), state.Pool, email[at+1:])
	if err != nil {
		writeInternalError(w, err)
		return
	}
	// Exactly one claimant, or none: an ambiguous domain is not routed anywhere.
	if len(tenants) != 1 || state.PublicAppURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"ssoAvailable": false}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"ssoAvailable": true, "tenantId": tenants[0],
		"startUrl": "/auth/sso/start?tenant=" + tenants[0].String()}})
}

func ssoStart(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	tenant, err := uuid.Parse(r.URL.Query().Get("tenant"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`tenant` must be a tenant id.", nil)
		return
	}
	cfg, err := tenantauth.OidcConfigFor(r.Context(), state.Pool, tenant)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if cfg == nil || state.PublicAppURL == "" {
		writeServiceError(w, http.StatusNotFound, errcode.NotFound, "Single sign-on is not set up for this workspace.", nil)
		return
	}
	secret, err := ssoClientSecret(r.Context(), state, cfg)
	if err != nil {
		writeServiceError(w, http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available right now.", nil)
		return
	}
	cfg.RedirectURI = ssoRedirectURI(state)
	authURL, csrf, nonce, verifier, err := tenantauth.OidcAuthorizeURL(r.Context(), cfg, secret)
	if err != nil {
		writeServiceError(w, http.StatusBadGateway, errcode.SSOFailed, "The identity provider could not be reached.", nil)
		return
	}
	if err := tenantauth.CreateSSOState(r.Context(), state.Pool, tenant, csrf, nonce, verifier, time.Now()); err != nil {
		writeInternalError(w, err)
		return
	}
	redirectTo(w, authURL)
}

func ssoCallback(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	q := r.URL.Query()
	if q.Get("error") != "" {
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed, "The identity provider refused the sign-in.", nil)
		return
	}
	now := time.Now()
	st, err := tenantauth.ConsumeSSOState(r.Context(), state.Pool, q.Get("state"), now)
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.SSOFailed, "The sign-in link is unknown, used or expired. Start again.", nil)
		return
	}
	cfg, err := tenantauth.OidcConfigFor(r.Context(), state.Pool, st.Tenant)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if cfg == nil {
		writeServiceError(w, http.StatusNotFound, errcode.NotFound, "Single sign-on is not set up for this workspace.", nil)
		return
	}
	secret, err := ssoClientSecret(r.Context(), state, cfg)
	if err != nil {
		writeServiceError(w, http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available right now.", nil)
		return
	}
	cfg.RedirectURI = ssoRedirectURI(state)
	who, err := tenantauth.OidcVerifyCallback(r.Context(), cfg, secret, q.Get("code"), st.Nonce, st.PKCEVerifier)
	if err != nil {
		slog.Warn("sso callback verification failed", "tenant_id", st.Tenant, "error", err)
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed, "The identity provider's answer could not be verified.", nil)
		return
	}
	if !cfg.EmailAllowed(who.Email) {
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed, "This email address is not allowed to sign in to this workspace.", nil)
		return
	}
	member, err := ssoMember(r.Context(), state, st.Tenant, cfg, who)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if member.Status != identity.StatusActive {
		writeServiceError(w, http.StatusForbidden, errcode.AccountDisabled, "This account is disabled.", nil)
		return
	}
	token, ok := issueSession(w, r, state, member.IdentityID,
		identity.Membership{MemberID: member.ID, TenantID: st.Tenant}, now)
	if !ok {
		return
	}
	_ = token
	dest := strings.TrimRight(state.PublicAppURL, "/") + "/"
	if SameOrigin(cfg.PostLoginRedirect, state.PublicAppURL) {
		dest = cfg.PostLoginRedirect
	}
	redirectTo(w, dest)
}

// ssoMember finds the member an IdP identity signs in as: the one already tied to this subject, an
// existing member with that address (tied now), or a new one provisioned with the default role.
func ssoMember(ctx context.Context, state *AppState, tenant uuid.UUID, cfg *tenantauth.OidcConfig, who *tenantauth.VerifiedIdentity) (*identity.Member, error) {
	if m, err := identity.FindExternalMember(ctx, state.Pool, tenant, ssoProvider, who.ExternalSubject); err != nil || m != nil {
		return m, err
	}
	if m, err := identity.MemberByEmail(ctx, state.Pool, tenant, who.Email); err == nil {
		if m.AuthProvider == identity.ServiceAccountProvider {
			return nil, errors.New("not a person")
		}
		if err := identity.LinkExternalSubject(ctx, state.Pool, tenant, m.ID, ssoProvider, who.ExternalSubject); err != nil {
			return nil, err
		}
		return m, nil
	} else if !errors.Is(err, identity.ErrNotFound) {
		return nil, err
	}
	m, _, err := identity.AddMember(ctx, state.Pool, identity.NewMember{TenantID: tenant, Email: who.Email,
		AuthProvider: ssoProvider, ExternalSubject: who.ExternalSubject})
	if err != nil {
		return nil, err
	}
	if cfg.DefaultRole != "" {
		if err := peripherals.AssignRole(ctx, state.Pool, tenant, m.ID, cfg.DefaultRole, nil); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func mountSSO(r chi.Router, state *AppState) {
	r.Get("/auth/sso/discover", func(w http.ResponseWriter, r *http.Request) { ssoDiscover(w, r, state) })
	r.Get("/auth/sso/start", func(w http.ResponseWriter, r *http.Request) { ssoStart(w, r, state) })
	r.Get("/auth/sso/callback", func(w http.ResponseWriter, r *http.Request) { ssoCallback(w, r, state) })
}
