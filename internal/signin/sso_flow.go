package signin

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	saml2 "github.com/russellhaering/gosaml2"

	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// SSOProviderOIDC is the external-subject namespace of members provisioned through OIDC.
const SSOProviderOIDC = "oidc"

// SameOrigin reports whether raw is an absolute http(s) URL on the same scheme, host and port as base.
func SameOrigin(raw, base string) bool {
	u, err := url.Parse(raw)
	b, err2 := url.Parse(base)
	if err != nil || err2 != nil || u.Host == "" || b.Host == "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == b.Scheme && strings.EqualFold(u.Host, b.Host)
}

func errSSONotSetUp() error {
	return serviceErr(http.StatusNotFound, errcode.NotFound, "Single sign-on is not set up for this workspace.", nil)
}

func errSSOUnavailable() error {
	return serviceErr(http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available right now.", nil)
}

func errSSOUnreachable() error {
	return serviceErr(http.StatusBadGateway, errcode.SSOFailed, "The identity provider could not be reached.", nil)
}

func errSSOUnverified() error {
	return serviceErr(http.StatusForbidden, errcode.SSOFailed, "The identity provider's answer could not be verified.", nil)
}

func errSSOState() error {
	return serviceErr(http.StatusBadRequest, errcode.SSOFailed, "The sign-in link is unknown, used or expired. Start again.", nil)
}

// SSODiscovery says which sign-in route an email address should use.
type SSODiscovery struct {
	Available bool
	TenantID  uuid.UUID
	Protocol  string
	StartURL  string
}

// SSODiscover routes an address to the one tenant that has claimed (and verified) its domain. An
// ambiguous domain is not routed anywhere.
func (s *Service) SSODiscover(ctx context.Context, email string) (*SSODiscovery, error) {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return nil, serviceErr(http.StatusBadRequest, errcode.ValidationFailed, "`email` must be an email address.", nil)
	}
	tenants, err := sso.TenantsForEmailDomain(ctx, s.d.Pool, email[at+1:])
	if err != nil {
		return nil, err
	}
	if len(tenants) != 1 || s.d.PublicURL == "" {
		return &SSODiscovery{}, nil
	}
	kind, err := sso.ProviderOf(ctx, s.d.Pool, tenants[0])
	if err != nil {
		return nil, err
	}
	start := "/auth/sso/start?tenant=" + tenants[0].String()
	if kind == sso.ProviderSAML {
		start = "/auth/saml/" + tenants[0].String() + "/start"
	}
	return &SSODiscovery{Available: true, TenantID: tenants[0], Protocol: string(kind), StartURL: start}, nil
}

func (s *Service) oidcRedirectURI() string {
	return strings.TrimRight(s.d.PublicURL, "/") + "/auth/sso/callback"
}

func (s *Service) oidcSecret(ctx context.Context, cfg *sso.OidcConfig) (string, error) {
	return s.d.Router.Secrets().GetSecret(ctx, cfg.ClientSecretRef)
}

// SSOStart is where to send the browser to sign in at the tenant's OIDC provider, and the state it
// carries: the HTTP layer binds it to the browser (a cookie) so the callback only counts from there.
type SSOStart struct {
	RedirectURL string
	State       string
}

// OIDCStart begins an OIDC sign-in: PKCE and nonce, kept in a single-use state row.
func (s *Service) OIDCStart(ctx context.Context, tenant uuid.UUID) (*SSOStart, error) {
	cfg, err := sso.OidcConfigFor(ctx, s.d.Pool, tenant)
	if err != nil {
		return nil, err
	}
	if cfg == nil || s.d.PublicURL == "" {
		return nil, errSSONotSetUp()
	}
	secret, err := s.oidcSecret(ctx, cfg)
	if err != nil {
		return nil, errSSOUnavailable()
	}
	cfg.RedirectURI = s.oidcRedirectURI()
	authURL, csrf, nonce, verifier, err := sso.OidcAuthorizeURL(ctx, cfg, secret)
	if err != nil {
		return nil, errSSOUnreachable()
	}
	if err := sso.CreateSSOState(ctx, s.d.Pool, tenant, csrf, nonce, verifier, s.now()); err != nil {
		return nil, err
	}
	return &SSOStart{RedirectURL: authURL, State: csrf}, nil
}

// SSOResult is a finished SSO sign-in: the session, and where the browser lands.
type SSOResult struct {
	Grant   *Grant
	Landing string
}

func (s *Service) landing(postLogin string) string {
	if SameOrigin(postLogin, s.d.PublicURL) {
		return postLogin
	}
	return strings.TrimRight(s.d.PublicURL, "/") + "/"
}

// OIDCCallbackRequest is what the provider sent the browser back with. SameBrowser says whether the
// request carries the binding the start set: a state is only valid from the browser that began it.
type OIDCCallbackRequest struct {
	Code, State string
	IdPError    bool
	SameBrowser bool
}

// OIDCCallback verifies the provider's answer and signs the person in.
func (s *Service) OIDCCallback(ctx context.Context, req OIDCCallbackRequest, c Client) (*SSOResult, error) {
	if req.IdPError {
		return nil, serviceErr(http.StatusForbidden, errcode.SSOFailed, "The identity provider refused the sign-in.", nil)
	}
	st, err := sso.ConsumeSSOState(ctx, s.d.Pool, req.State, s.now())
	if err != nil {
		return nil, errSSOState()
	}
	// The state is spent either way; it only counts from the browser that started the sign-in.
	if !req.SameBrowser {
		return nil, serviceErr(http.StatusBadRequest, errcode.SSOFailed, "This sign-in was started in another browser. Start again here.", nil)
	}
	cfg, err := sso.OidcConfigFor(ctx, s.d.Pool, st.Tenant)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errSSONotSetUp()
	}
	secret, err := s.oidcSecret(ctx, cfg)
	if err != nil {
		return nil, errSSOUnavailable()
	}
	cfg.RedirectURI = s.oidcRedirectURI()
	who, err := sso.OidcVerifyCallback(ctx, cfg, secret, req.Code, st.Nonce, st.PKCEVerifier)
	if err != nil {
		slog.Warn("sso callback verification failed", "tenant_id", st.Tenant, "error", err)
		return nil, errSSOUnverified()
	}
	g, err := s.FinishSSO(ctx, st.Tenant, SSOPolicy{Provider: SSOProviderOIDC, DefaultRole: cfg.DefaultRole,
		Mapping: cfg.Mapping(), Allowed: cfg.EmailAllowed}, who, c)
	if err != nil {
		return nil, err
	}
	return &SSOResult{Grant: g, Landing: s.landing(cfg.PostLoginRedirect)}, nil
}

// samlProvider loads the tenant's enabled SAML settings and builds the service provider for them.
func (s *Service) samlProvider(ctx context.Context, tenant uuid.UUID) (*sso.SamlConfig, *saml2.SAMLServiceProvider, error) {
	cfg, err := sso.SamlConfigFor(ctx, s.d.Pool, tenant)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil || s.d.PublicURL == "" {
		return nil, nil, errSSONotSetUp()
	}
	sp, err := cfg.ServiceProvider(sso.SamlURLsFor(s.d.PublicURL, tenant))
	if err != nil {
		slog.Error("saml: building the service provider", "tenant_id", tenant, "error", err)
		return nil, nil, errSSOUnavailable()
	}
	return cfg, sp, nil
}

// SAMLMetadata is what an admin registers at the identity provider: this service's entity ID and
// assertion consumer URL for the tenant.
func (s *Service) SAMLMetadata(ctx context.Context, tenant uuid.UUID) ([]byte, error) {
	cfg, _, err := s.samlProvider(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return sso.ServiceProviderMetadata(sso.SamlURLsFor(s.d.PublicURL, tenant), cfg)
}

// SAMLStart begins a SAML sign-in and returns where to send the browser. The relay state is our own
// single-use state row; the AuthnRequest's ID rides in it so the response can be bound to this
// sign-in (InResponseTo).
func (s *Service) SAMLStart(ctx context.Context, tenant uuid.UUID) (string, error) {
	_, sp, err := s.samlProvider(ctx, tenant)
	if err != nil {
		return "", err
	}
	relay := sso.NewStateToken()
	target, requestID, err := sso.StartRedirect(sp, relay)
	if err != nil {
		slog.Error("saml: building the sign-in request", "tenant_id", tenant, "error", err)
		return "", errSSOUnreachable()
	}
	if err := sso.CreateSSOState(ctx, s.d.Pool, tenant, relay, requestID, "", s.now()); err != nil {
		return "", err
	}
	return target.String(), nil
}

// SAMLACS checks the provider's signed response against the single-use request this sign-in made, with
// the provider's own certificates, and signs the person in.
func (s *Service) SAMLACS(ctx context.Context, tenant uuid.UUID, relayState, samlResponse string, c Client) (*SSOResult, error) {
	cfg, sp, err := s.samlProvider(ctx, tenant)
	if err != nil {
		return nil, err
	}
	st, err := sso.ConsumeSSOState(ctx, s.d.Pool, relayState, time.Now())
	if err != nil || st.Tenant != tenant {
		return nil, serviceErr(http.StatusBadRequest, errcode.SSOFailed, "The sign-in is unknown, used or expired. Start again.", nil)
	}
	who, err := cfg.VerifyResponse(sp, samlResponse, st.Nonce)
	if err != nil {
		slog.Warn("saml response verification failed", "tenant_id", tenant, "error", err)
		return nil, errSSOUnverified()
	}
	g, err := s.FinishSSO(ctx, tenant, SSOPolicy{Provider: sso.ProviderSAML, DefaultRole: cfg.DefaultRole,
		Mapping: cfg.Mapping(), Allowed: cfg.EmailAllowed}, who, c)
	if err != nil {
		return nil, err
	}
	return &SSOResult{Grant: g, Landing: s.landing(cfg.PostLoginRedirect)}, nil
}
