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
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	saml2 "github.com/russellhaering/gosaml2"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/signin"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// SSOSecretKey is the config key a tenant's OIDC client secret is stored under (control.TenantSecretRef).
const SSOSecretKey = "sso.clientSecret"

const ssoProvider = "oidc"

// ssoRequired refuses a password sign-in (or tenant switch) into a tenant that enforces SSO, unless
// the member is the tenant's owner — the way back in when the IdP is broken.
func ssoRequired(w http.ResponseWriter, r *http.Request, state *AppState, m identity.Membership) bool {
	if err := state.Signin().RequirePasswordAllowed(r.Context(), m); err != nil {
		writeCrudError(w, err)
		return true
	}
	return false
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
	kind, err := tenantauth.ProviderOf(r.Context(), state.Pool, tenants[0])
	if err != nil {
		writeInternalError(w, err)
		return
	}
	start := "/auth/sso/start?tenant=" + tenants[0].String()
	if kind == tenantauth.ProviderSAML {
		start = "/auth/saml/" + tenants[0].String() + "/start"
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"ssoAvailable": true, "tenantId": tenants[0], "protocol": kind, "startUrl": start}})
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
	http.SetCookie(w, ssoBindCookie(csrf, state.CookieSecure))
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
	// The state is spent either way. It only counts from the browser that started the sign-in.
	http.SetCookie(w, clearSSOBindCookie(state.CookieSecure))
	if !ssoBoundToBrowser(r, q.Get("state")) {
		writeServiceError(w, http.StatusBadRequest, errcode.SSOFailed, "This sign-in was started in another browser. Start again here.", nil)
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
	finishSSO(w, r, state, st.Tenant, ssoPolicy{SSOPolicy: signin.SSOPolicy{Provider: ssoProvider, DefaultRole: cfg.DefaultRole,
		Mapping: cfg.Mapping(), Allowed: cfg.EmailAllowed}, postLoginRedirect: cfg.PostLoginRedirect}, who)
}

// ssoPolicy is what a tenant's provider settings decide once somebody has been verified, whichever
// protocol verified them: signin.SSOPolicy plus where the browser lands afterwards.
type ssoPolicy struct {
	signin.SSOPolicy
	postLoginRedirect string
}

// finishSSO turns a verified identity into a session (signin.Service.FinishSSO), sets the session
// cookies and redirects into the app.
func finishSSO(w http.ResponseWriter, r *http.Request, state *AppState, tenant uuid.UUID, p ssoPolicy, who *tenantauth.VerifiedIdentity) {
	grant, err := state.Signin().FinishSSO(r.Context(), tenant, p.SSOPolicy, who, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	setSessionCookies(w, state, grant)
	dest := strings.TrimRight(state.PublicAppURL, "/") + "/"
	if SameOrigin(p.postLoginRedirect, state.PublicAppURL) {
		dest = p.postLoginRedirect
	}
	redirectTo(w, dest)
}

// mountSSO: the discovery and the two sign-in starts share the credential-guessing bucket (each start
// writes a state row and, for OIDC, calls the tenant's identity provider); the callbacks and the
// SAML metadata are answers to a browser or to the IdP and stay on the general bucket.
func mountSSO(r *router.Router, state *AppState, guard router.Middleware) {
	acc := AccessGroups(r, state)
	guarded := r.Labelled(LabelPublic, guard)
	acc.Public.Get("/auth/saml/{tenant}/metadata", func(w http.ResponseWriter, r *http.Request) { samlMetadata(w, r, state) })
	guarded.Get("/auth/saml/{tenant}/start", func(w http.ResponseWriter, r *http.Request) { samlStart(w, r, state) })
	acc.Public.Post("/auth/saml/{tenant}/acs", func(w http.ResponseWriter, r *http.Request) { samlACS(w, r, state) })
	guarded.Get("/auth/sso/discover", func(w http.ResponseWriter, r *http.Request) { ssoDiscover(w, r, state) })
	guarded.Get("/auth/sso/start", func(w http.ResponseWriter, r *http.Request) { ssoStart(w, r, state) })
	acc.Public.Get("/auth/sso/callback", func(w http.ResponseWriter, r *http.Request) { ssoCallback(w, r, state) })
}

// samlProvider loads the tenant's enabled SAML settings and builds the service provider for them.
func samlProvider(w http.ResponseWriter, r *http.Request, state *AppState) (uuid.UUID, *tenantauth.SamlConfig, *saml2.SAMLServiceProvider, bool) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return uuid.Nil, nil, nil, false
	}
	tenant, err := uuid.Parse(r.PathValue("tenant"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "The tenant in the path is not a tenant id.", nil)
		return uuid.Nil, nil, nil, false
	}
	cfg, err := tenantauth.SamlConfigFor(r.Context(), state.Pool, tenant)
	if err != nil {
		writeInternalError(w, err)
		return uuid.Nil, nil, nil, false
	}
	if cfg == nil || state.PublicAppURL == "" {
		writeServiceError(w, http.StatusNotFound, errcode.NotFound, "Single sign-on is not set up for this workspace.", nil)
		return uuid.Nil, nil, nil, false
	}
	sp, err := cfg.ServiceProvider(tenantauth.SamlURLsFor(state.PublicAppURL, tenant))
	if err != nil {
		slog.Error("saml: building the service provider", "tenant_id", tenant, "error", err)
		writeServiceError(w, http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available right now.", nil)
		return uuid.Nil, nil, nil, false
	}
	return tenant, cfg, sp, true
}

// samlMetadata is what an admin registers at the identity provider: this service's entity ID and
// assertion consumer URL for the tenant.
func samlMetadata(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, _, _, ok := samlProvider(w, r, state)
	if !ok {
		return
	}
	out, err := tenantauth.ServiceProviderMetadata(tenantauth.SamlURLsFor(state.PublicAppURL, tenant))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(out)
}

func samlStart(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, _, sp, ok := samlProvider(w, r, state)
	if !ok {
		return
	}
	// The relay state is our own single-use state row; the AuthnRequest's ID rides in it so the
	// response can be bound to this sign-in (InResponseTo).
	relay := tenantauth.NewStateToken()
	target, requestID, err := tenantauth.StartRedirect(sp, relay)
	if err != nil {
		slog.Error("saml: building the sign-in request", "tenant_id", tenant, "error", err)
		writeServiceError(w, http.StatusBadGateway, errcode.SSOFailed, "The identity provider could not be reached.", nil)
		return
	}
	if err := tenantauth.CreateSSOState(r.Context(), state.Pool, tenant, relay, requestID, "", time.Now()); err != nil {
		writeInternalError(w, err)
		return
	}
	redirectTo(w, target.String())
}

// samlACS receives the provider's signed response.
func samlACS(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, cfg, sp, ok := samlProvider(w, r, state)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "The response is not a form post.", nil)
		return
	}
	st, err := tenantauth.ConsumeSSOState(r.Context(), state.Pool, r.PostForm.Get("RelayState"), time.Now())
	if err != nil || st.Tenant != tenant {
		writeServiceError(w, http.StatusBadRequest, errcode.SSOFailed, "The sign-in is unknown, used or expired. Start again.", nil)
		return
	}
	// What is checked is the response against the single-use request this sign-in made, with the
	// provider's own certificates; the destination and recipient are our registered ACS URL.
	who, err := cfg.VerifyResponse(sp, r.PostForm.Get("SAMLResponse"), st.Nonce)
	if err != nil {
		slog.Warn("saml response verification failed", "tenant_id", tenant, "error", err)
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed, "The identity provider's answer could not be verified.", nil)
		return
	}
	finishSSO(w, r, state, tenant, ssoPolicy{SSOPolicy: signin.SSOPolicy{Provider: tenantauth.ProviderSAML, DefaultRole: cfg.DefaultRole,
		Mapping: cfg.Mapping(), Allowed: cfg.EmailAllowed}, postLoginRedirect: cfg.PostLoginRedirect}, who)
}
