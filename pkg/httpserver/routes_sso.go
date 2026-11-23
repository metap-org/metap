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

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	saml2 "github.com/russellhaering/gosaml2"

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
	finishSSO(w, r, state, st.Tenant, ssoPolicy{provider: ssoProvider, defaultRole: cfg.DefaultRole, mapping: cfg.Mapping(),
		postLoginRedirect: cfg.PostLoginRedirect, allowed: cfg.EmailAllowed}, who)
}

// ssoPolicy is what a tenant's provider settings decide once somebody has been verified, whichever
// protocol verified them.
type ssoPolicy struct {
	provider          string // the external-subject namespace: "oidc" or "saml"
	defaultRole       string
	mapping           tenantauth.RoleMapping
	postLoginRedirect string
	allowed           func(address string) bool
}

// finishSSO turns a verified identity into a session: domain restriction, member lookup or
// creation, role sync, then the session cookie and the redirect into the app.
func finishSSO(w http.ResponseWriter, r *http.Request, state *AppState, tenant uuid.UUID, p ssoPolicy, who *tenantauth.VerifiedIdentity) {
	now := time.Now()
	if !p.allowed(who.Email) {
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed, "This email address is not allowed to sign in to this workspace.", nil)
		return
	}
	member, err := ssoMember(r.Context(), state, tenant, p, who)
	if errors.Is(err, errSSOEmailUnverified) {
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed,
			"The identity provider has not verified this email address, so it cannot be used to sign in.", nil)
		return
	}
	if errors.Is(err, errSSODomainUnverified) {
		writeServiceError(w, http.StatusForbidden, errcode.SSOFailed,
			"This workspace has not verified that it owns the domain of your email address.", nil)
		return
	}
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if member.Status != identity.StatusActive {
		writeServiceError(w, http.StatusForbidden, errcode.AccountDisabled, "This account is disabled.", nil)
		return
	}
	if err := syncSSORoles(r.Context(), state, tenant, member, p.mapping, who.Roles); err != nil {
		slog.Error("sso: syncing roles from the identity provider", "tenant_id", tenant, "error", err)
		writeServiceError(w, http.StatusInternalServerError, errcode.SSOFailed, "Your roles could not be applied. Try again.", nil)
		return
	}
	token, ok := issueSession(w, r, state, member.IdentityID,
		identity.Membership{MemberID: member.ID, TenantID: tenant}, now)
	if !ok {
		return
	}
	_ = token
	dest := strings.TrimRight(state.PublicAppURL, "/") + "/"
	if SameOrigin(p.postLoginRedirect, state.PublicAppURL) {
		dest = p.postLoginRedirect
	}
	redirectTo(w, dest)
}

// errSSOEmailUnverified: the provider did not vouch for the address, so it may not be used to find,
// link or create a member.
var errSSOEmailUnverified = errors.New("the identity provider has not verified the email address")

// errSSODomainUnverified: the workspace has not proved it owns the address's domain, so the
// provider's say-so is not enough to find, link or create a member with that address.
var errSSODomainUnverified = errors.New("the workspace has not verified the email domain")

// syncSSORoles brings the roles the provider's claim decides (cfg.RoleMap's values) in line with
// the claim, at every sign-in. Roles outside the map are left alone, and the owner keeps theirs:
// the provider cannot lock the person who is the way back in out of their own workspace.
func syncSSORoles(ctx context.Context, state *AppState, tenant uuid.UUID, member *identity.Member, mapping tenantauth.RoleMapping, claimValues []string) error {
	managed := mapping.ManagedRoles()
	if mapping.Claim == "" || len(managed) == 0 {
		return nil
	}
	want := mapping.MappedRoles(claimValues)
	have, err := peripherals.GetRolesForUser(ctx, state.Pool, tenant, member.ID)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, role := range have {
		held[role] = true
	}
	owner, err := identity.OwnerID(ctx, state.Pool, tenant)
	if err != nil {
		return err
	}
	for _, role := range managed {
		switch {
		case want[role] && !held[role]:
			if err := peripherals.AssignRole(ctx, state.Pool, tenant, member.ID, role, nil); err != nil {
				return err
			}
		case !want[role] && held[role] && owner != member.ID:
			if err := peripherals.RevokeRole(ctx, state.Pool, tenant, member.ID, role); err != nil {
				return err
			}
		}
	}
	return nil
}

// ssoMember finds the member an IdP identity signs in as: the one already tied to this subject, an
// existing member with that address (tied now), or a new one provisioned with the default role.
func ssoMember(ctx context.Context, state *AppState, tenant uuid.UUID, p ssoPolicy, who *tenantauth.VerifiedIdentity) (*identity.Member, error) {
	if m, err := identity.FindExternalMember(ctx, state.Pool, tenant, p.provider, who.ExternalSubject); err != nil || m != nil {
		return m, err
	}
	// From here on the address is what identifies the person, so the provider must vouch for it.
	if !who.EmailVerified {
		return nil, errSSOEmailUnverified
	}
	if ok, err := tenantauth.DomainVerifiedFor(ctx, state.Pool, tenant, who.Email); err != nil {
		return nil, err
	} else if !ok {
		return nil, errSSODomainUnverified
	}
	if m, err := identity.MemberByEmail(ctx, state.Pool, tenant, who.Email); err == nil {
		if m.AuthProvider == identity.ServiceAccountProvider {
			return nil, errors.New("not a person")
		}
		if err := identity.LinkExternalSubject(ctx, state.Pool, tenant, m.ID, p.provider, who.ExternalSubject); err != nil {
			return nil, err
		}
		return m, nil
	} else if !errors.Is(err, identity.ErrNotFound) {
		return nil, err
	}
	m, _, err := identity.AddMember(ctx, state.Pool, identity.NewMember{TenantID: tenant, Email: who.Email,
		AuthProvider: p.provider, ExternalSubject: who.ExternalSubject})
	if err != nil {
		return nil, err
	}
	if p.defaultRole != "" {
		if err := peripherals.AssignRole(ctx, state.Pool, tenant, m.ID, p.defaultRole, nil); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func mountSSO(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	acc.Public.Get("/auth/saml/{tenant}/metadata", func(w http.ResponseWriter, r *http.Request) { samlMetadata(w, r, state) })
	acc.Public.Get("/auth/saml/{tenant}/start", func(w http.ResponseWriter, r *http.Request) { samlStart(w, r, state) })
	acc.Public.Post("/auth/saml/{tenant}/acs", func(w http.ResponseWriter, r *http.Request) { samlACS(w, r, state) })
	acc.Public.Get("/auth/sso/discover", func(w http.ResponseWriter, r *http.Request) { ssoDiscover(w, r, state) })
	acc.Public.Get("/auth/sso/start", func(w http.ResponseWriter, r *http.Request) { ssoStart(w, r, state) })
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
	finishSSO(w, r, state, tenant, ssoPolicy{provider: tenantauth.ProviderSAML, defaultRole: cfg.DefaultRole, mapping: cfg.Mapping(),
		postLoginRedirect: cfg.PostLoginRedirect, allowed: cfg.EmailAllowed}, who)
}
