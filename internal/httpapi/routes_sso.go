// Single sign-on over OIDC and SAML (roadmap 115 M7). A tenant admin configures the IdP through
// the SSO admin service; people then sign in through these public REST routes (a browser redirect
// flow has no typed-operation shape):
//
//	GET  /auth/sso/discover?email=   which sign-in route this address should use
//	GET  /auth/sso/start?tenant=     redirect to the tenant's OIDC provider
//	GET  /auth/sso/callback          the OIDC provider sends the browser back here; a session starts
//	GET  /auth/saml/{tenant}/start, POST .../acs, GET .../metadata   the SAML equivalents
//
// The protocol exchange, the member lookup and the session are internal/signin's; what is here is
// the browser's side: query and form values, the binding cookie, the redirects.
//
// MFA is not asked again after SSO: the IdP is the second factor (as for the other external
// providers). A tenant that enforces SSO refuses password sign-in for everyone but its owner.
package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/internal/signin"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// SSOSecretKey is the config key a tenant's OIDC client secret is stored under (control.TenantSecretRef).
const SSOSecretKey = "sso.clientSecret"

// redirectTo sends the browser to target. Neither caller passes anything taken from the request:
// the sign-in URL comes from the tenant's configured identity provider and the landing page is
// checked to be on this service's own origin (signin.SameOrigin) — which is why this sets the header
// itself rather than going through http.Redirect with the request.
func redirectTo(w http.ResponseWriter, target string) {
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

// SameOrigin reports whether raw is an absolute http(s) URL on the same scheme, host and port as base.
func SameOrigin(raw, base string) bool { return signin.SameOrigin(raw, base) }

func ssoDiscover(w http.ResponseWriter, r *http.Request, state *AppState) {
	d, err := state.Signin().SSODiscover(r.Context(), r.URL.Query().Get("email"))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	if !d.Available {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"ssoAvailable": false}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"ssoAvailable": true, "tenantId": d.TenantID, "protocol": d.Protocol, "startUrl": d.StartURL}})
}

func ssoStart(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	tenant, err := uuid.Parse(r.URL.Query().Get("tenant"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`tenant` must be a tenant id.", nil)
		return
	}
	start, err := state.Signin().OIDCStart(r.Context(), tenant)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	http.SetCookie(w, ssoBindCookie(start.State, state.CookieSecure))
	redirectTo(w, start.RedirectURL)
}

func ssoCallback(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	q := r.URL.Query()
	// The state is spent whatever happens, so the binding cookie goes with it.
	http.SetCookie(w, clearSSOBindCookie(state.CookieSecure))
	res, err := state.Signin().OIDCCallback(r.Context(), signin.OIDCCallbackRequest{
		Code: q.Get("code"), State: q.Get("state"), IdPError: q.Get("error") != "", SameBrowser: ssoBoundToBrowser(r, q.Get("state")),
	}, clientOf(r, state))
	finishSSO(w, state, res, err)
}

// finishSSO sets the session cookies and redirects into the app, or answers the failure.
func finishSSO(w http.ResponseWriter, state *AppState, res *signin.SSOResult, err error) {
	if err != nil {
		writeCrudError(w, err)
		return
	}
	setSessionCookies(w, state, res.Grant)
	redirectTo(w, res.Landing)
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

// samlTenant is the tenant in the path of a SAML route; a service that issues no tokens answers 501.
func samlTenant(w http.ResponseWriter, r *http.Request, state *AppState) (uuid.UUID, bool) {
	if !requireIssuer(w, state) {
		return uuid.Nil, false
	}
	tenant, err := uuid.Parse(r.PathValue("tenant"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "The tenant in the path is not a tenant id.", nil)
		return uuid.Nil, false
	}
	return tenant, true
}

// samlMetadata is what an admin registers at the identity provider.
func samlMetadata(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, ok := samlTenant(w, r, state)
	if !ok {
		return
	}
	out, err := state.Signin().SAMLMetadata(r.Context(), tenant)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(out)
}

func samlStart(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, ok := samlTenant(w, r, state)
	if !ok {
		return
	}
	start, err := state.Signin().SAMLStart(r.Context(), tenant)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	http.SetCookie(w, samlBindCookie(start.State))
	redirectTo(w, start.RedirectURL)
}

// samlACS receives the provider's signed response.
func samlACS(w http.ResponseWriter, r *http.Request, state *AppState) {
	tenant, ok := samlTenant(w, r, state)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "The response is not a form post.", nil)
		return
	}
	relay := r.PostForm.Get("RelayState")
	// The state is spent whatever happens, so the binding cookie goes with it.
	http.SetCookie(w, clearSAMLBindCookie())
	res, err := state.Signin().SAMLACS(r.Context(), tenant, relay, r.PostForm.Get("SAMLResponse"),
		samlBoundToBrowser(r, relay), clientOf(r, state))
	finishSSO(w, state, res, err)
}
