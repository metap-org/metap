package adminapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// AuditEntitySSO is the audit-trail entity SSO configuration changes are recorded under (the record is the tenant).
const AuditEntitySSO = "metadata.sso_config"

// ssoSecretKey is the config key the OIDC client secret is stored under; keep in step with httpserver.SSOSecretKey.
const ssoSecretKey = "sso.clientSecret"

var emailDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$`)

// SsoAdminService is roadmap 115 M7: a tenant connects its identity provider.
type SsoAdminService struct {
	d identityDeps
}

func NewSsoAdminService(cfg IdentityConfig) *SsoAdminService {
	return &SsoAdminService{d: identityDeps{cfg: cfg}}
}

func (s *SsoAdminService) redirectURI() string {
	if s.d.cfg.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.d.cfg.PublicURL, "/") + "/auth/sso/callback"
}

func (s *SsoAdminService) view(ctx context.Context, tenant uuid.UUID) (*SsoConfig, error) {
	cfg, enabled, err := tenantauth.StoredOidcConfig(ctx, s.d.cfg.Shared, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := &SsoConfig{RedirectUri: s.redirectURI()}
	if cfg == nil {
		return out, nil
	}
	_, secretErr := s.d.cfg.Router.Secrets().GetSecret(ctx, cfg.ClientSecretRef)
	out.Configured, out.Enabled = true, enabled
	out.IssuerUrl, out.ClientId, out.HasClientSecret = cfg.IssuerURL, cfg.ClientID, secretErr == nil
	out.Scopes, out.EmailDomains, out.DefaultRole = cfg.Scopes, cfg.EmailDomains, cfg.DefaultRole
	out.Enforced, out.PostLoginRedirect = cfg.Enforced, cfg.PostLoginRedirect
	return out, nil
}

func (s *SsoAdminService) GetSsoConfig(ctx context.Context, _ *Empty) (*SsoConfig, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.tenant)
}

func sameOrigin(raw, base string) bool {
	u, err := url.Parse(raw)
	b, err2 := url.Parse(base)
	if err != nil || err2 != nil || u.Host == "" || b.Host == "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == b.Scheme && strings.EqualFold(u.Host, b.Host)
}

func (s *SsoAdminService) SetSsoConfig(ctx context.Context, req *SetSsoConfigRequest) (*SsoConfig, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	bad := func(msg string) (*SsoConfig, error) {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, msg)
	}
	if s.redirectURI() == "" {
		return nil, svcErr(ctx, http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available: this deployment has no public URL.")
	}
	issuer := strings.TrimSpace(req.GetIssuerUrl())
	// Plain http is accepted only for loopback (local development IdPs).
	if u, err := url.Parse(issuer); err != nil || !(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) || u.Host == "" {
		return bad("`issuerUrl` must be an https URL.")
	}
	clientID := strings.TrimSpace(req.GetClientId())
	if clientID == "" || len(clientID) > 200 {
		return bad("`clientId` is required.")
	}
	var domains []string
	for _, d := range req.GetEmailDomains() {
		d = strings.ToLower(strings.TrimSpace(d))
		if !emailDomainPattern.MatchString(d) {
			return bad("`" + d + "` is not an email domain.")
		}
		domains = append(domains, d)
	}
	if len(domains) > 20 {
		return bad("At most 20 email domains.")
	}
	// The landing page must be on the service's own origin: the callback redirects there with a
	// fresh session cookie, so it cannot point anywhere else.
	if redirect := strings.TrimSpace(req.GetPostLoginRedirect()); redirect != "" && !sameOrigin(redirect, s.d.cfg.PublicURL) {
		return bad("`postLoginRedirect` must be a URL on " + s.d.cfg.PublicURL + ".")
	}
	role := strings.TrimSpace(req.GetDefaultRole())
	if role == permission.RoleAdmin {
		return bad("People provisioned by single sign-on cannot start as admin.")
	}
	if role != "" {
		if err := s.d.checkRoles(ctx, c.tenant, []string{role}); err != nil {
			return bad("`defaultRole` must be a role of this tenant.")
		}
	}
	if req.GetEnforced() {
		if !req.GetEnabled() {
			return bad("Single sign-on must be enabled to be enforced.")
		}
		owner, err := s.d.ensureOwner(ctx, c.tenant)
		if err != nil {
			return nil, rpcError(ctx, err)
		}
		if owner == uuid.Nil || c.self != owner {
			return nil, svcErr(ctx, http.StatusForbidden, errcode.OwnerProtected,
				"Only the tenant owner can require single sign-on: the owner keeps password sign-in as the way back.")
		}
	}

	ref := control.TenantSecretRef(c.tenant, ssoSecretKey)
	secret := req.GetClientSecret()
	store := s.d.cfg.Router.Secrets()
	if secret == "" {
		existing, err := store.GetSecret(ctx, ref)
		if err != nil {
			return bad("`clientSecret` is required the first time.")
		}
		secret = existing
	}
	scopes := req.GetScopes()
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	cfg := &tenantauth.OidcConfig{IssuerURL: issuer, ClientID: clientID, ClientSecretRef: ref, RedirectURI: s.redirectURI(),
		Scopes: scopes, PostLoginRedirect: strings.TrimSpace(req.GetPostLoginRedirect()), EmailDomains: domains,
		DefaultRole: role, Enforced: req.GetEnforced()}
	// Catch a wrong issuer now, not at someone's first sign-in.
	if _, _, _, _, err := tenantauth.OidcAuthorizeURL(ctx, cfg, secret); err != nil {
		return bad("The identity provider could not be reached or is not an OpenID Connect issuer.")
	}
	before, _, _ := tenantauth.StoredOidcConfig(ctx, s.d.cfg.Shared, c.tenant)
	if req.GetClientSecret() != "" {
		if err := store.PutSecret(ctx, ref, req.GetClientSecret()); err != nil {
			return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
		}
	}
	if err := tenantauth.SaveOidcConfig(ctx, s.d.cfg.Shared, c.tenant, cfg, req.GetEnabled()); err != nil {
		return nil, rpcError(ctx, err)
	}
	diff := map[string][2]any{"issuerUrl": {nil, issuer}, "enabled": {nil, req.GetEnabled()}, "enforced": {nil, req.GetEnforced()}}
	if before != nil {
		diff = map[string][2]any{"issuerUrl": {before.IssuerURL, issuer}, "enforced": {before.Enforced, req.GetEnforced()}}
	}
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionUpdate, diff)
	return s.view(ctx, c.tenant)
}

func (s *SsoAdminService) DeleteSsoConfig(ctx context.Context, _ *Empty) (*Empty, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	// Removing an enforced configuration is how the owner (or an admin) restores password sign-in.
	if err := tenantauth.DeleteOidcConfig(ctx, s.d.cfg.Shared, c.tenant); err != nil {
		return nil, rpcError(ctx, err)
	}
	_ = s.d.cfg.Router.Secrets().DeleteSecret(ctx, control.TenantSecretRef(c.tenant, ssoSecretKey))
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionDelete, map[string][2]any{"sso": {"configured", nil}})
	return &Empty{}, nil
}
