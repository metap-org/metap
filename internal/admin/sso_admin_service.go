package admin

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/secrets"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntitySSO is the audit-trail entity SSO configuration changes are recorded under (the record is the tenant).
const AuditEntitySSO = "metadata.sso_config"

// ssoSecretKey is the config key the OIDC client secret is stored under; keep in step with httpapi.SSOSecretKey.
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
	cfg, enabled, err := sso.StoredOidcConfig(ctx, s.d.cfg.Shared, tenant)
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
	out.RoleClaim, out.RoleMap = cfg.RoleClaim, cfg.RoleMap
	if out.Domains, err = s.domainsView(ctx, tenant); err != nil {
		return nil, err
	}
	if out.RoleMap == nil {
		out.RoleMap = map[string]string{}
	}
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
	c, err := adminDurable(s.d, ctx)
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
	access, err := s.validateAccess(ctx, c, accessInput{
		EmailDomains: req.GetEmailDomains(), PostLoginRedirect: req.GetPostLoginRedirect(), DefaultRole: req.GetDefaultRole(),
		RoleClaim: req.GetRoleClaim(), RoleMap: req.GetRoleMap(), Enabled: req.GetEnabled(), Enforced: req.GetEnforced()})
	if err != nil {
		return nil, err
	}
	domains, role, roleClaim, roleMap := access.domains, access.defaultRole, access.roleClaim, access.roleMap
	if other, err := sso.SamlConfigFor(ctx, s.d.cfg.Shared, c.tenant); err != nil {
		return nil, rpcError(ctx, err)
	} else if other != nil && req.GetEnabled() {
		return bad("SAML single sign-on is on for this workspace: turn it off before enabling OpenID Connect.")
	}

	ref := secrets.TenantSecretRef(c.tenant, ssoSecretKey)
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
	cfg := &sso.OidcConfig{IssuerURL: issuer, ClientID: clientID, ClientSecretRef: ref, RedirectURI: s.redirectURI(),
		Scopes: scopes, PostLoginRedirect: access.postLoginRedirect, EmailDomains: domains,
		DefaultRole: role, Enforced: req.GetEnforced(), RoleClaim: roleClaim, RoleMap: roleMap}
	// Catch a wrong issuer now, not at someone's first sign-in.
	if _, _, _, _, err := sso.OidcAuthorizeURL(ctx, cfg, secret); err != nil {
		return bad("The identity provider could not be reached or is not an OpenID Connect issuer.")
	}
	before, _, _ := sso.StoredOidcConfig(ctx, s.d.cfg.Shared, c.tenant)
	if req.GetClientSecret() != "" {
		if err := store.PutSecret(ctx, ref, req.GetClientSecret()); err != nil {
			return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
		}
	}
	if err := sso.SaveOidcConfig(ctx, s.d.cfg.Shared, c.tenant, cfg, req.GetEnabled()); err != nil {
		return nil, rpcError(ctx, err)
	}
	if err := sso.SyncDomains(ctx, s.d.cfg.Shared, c.tenant, domains); err != nil {
		return nil, rpcError(ctx, err)
	}
	diff := map[string][2]any{"issuerUrl": {nil, issuer}, "enabled": {nil, req.GetEnabled()}, "enforced": {nil, req.GetEnforced()}}
	if before != nil {
		diff = map[string][2]any{"issuerUrl": {before.IssuerURL, issuer}, "enforced": {before.Enforced, req.GetEnforced()}}
	}
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionUpdate, diff)
	return s.view(ctx, c.tenant)
}

// accessInput is what both protocols share: who may sign in, what they start as, which provider
// groups mean which roles, and whether passwords stop working.
type accessInput struct {
	EmailDomains      []string
	PostLoginRedirect string
	DefaultRole       string
	RoleClaim         string
	RoleMap           map[string]string
	Enabled, Enforced bool
}

type accessSettings struct {
	domains           []string
	postLoginRedirect string
	defaultRole       string
	roleClaim         string
	roleMap           map[string]string
}

// validateAccess checks and normalises the shared settings; every refusal is a 400 (or, for
// enforcing, a 403) with the reason.
func (s *SsoAdminService) validateAccess(ctx context.Context, c caller, in accessInput) (*accessSettings, error) {
	bad := func(msg string) (*accessSettings, error) {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, msg)
	}
	var domains []string
	for _, d := range in.EmailDomains {
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
	redirect := strings.TrimSpace(in.PostLoginRedirect)
	if redirect != "" && !sameOrigin(redirect, s.d.cfg.PublicURL) {
		return bad("`postLoginRedirect` must be a URL on " + s.d.cfg.PublicURL + ".")
	}
	role := strings.TrimSpace(in.DefaultRole)
	if role == permission.RoleAdmin {
		return bad("People provisioned by single sign-on cannot start as admin.")
	}
	if role != "" {
		if err := s.d.checkRoles(ctx, c.tenant, []string{role}); err != nil {
			return bad("`defaultRole` must be a role of this tenant.")
		}
	}
	roleClaim := strings.TrimSpace(in.RoleClaim)
	roleMap := map[string]string{}
	for value, mapped := range in.RoleMap {
		value, mapped = strings.TrimSpace(value), strings.TrimSpace(mapped)
		if value == "" || mapped == "" {
			return bad("`roleMap` needs a provider value and a role on every line.")
		}
		roleMap[value] = mapped
	}
	if len(roleMap) > 50 {
		return bad("At most 50 role mappings.")
	}
	if len(roleMap) > 0 && roleClaim == "" {
		return bad("`roleClaim` is required when `roleMap` is set.")
	}
	for _, mapped := range roleMap {
		// Like `defaultRole`: the provider never decides who administers the workspace.
		if mapped == permission.RoleAdmin || mapped == RolePlatformAdmin {
			return bad("A provider group cannot be mapped to " + mapped + ".")
		}
		if err := s.d.checkRoles(ctx, c.tenant, []string{mapped}); err != nil {
			return bad("`roleMap` names a role this tenant does not have: " + mapped + ".")
		}
	}
	if in.Enforced {
		if !in.Enabled {
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
	return &accessSettings{domains: domains, postLoginRedirect: redirect, defaultRole: role, roleClaim: roleClaim, roleMap: roleMap}, nil
}

func (s *SsoAdminService) DeleteSsoConfig(ctx context.Context, _ *Empty) (*Empty, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	// Removing an enforced configuration is how the owner (or an admin) restores password sign-in.
	if err := sso.DeleteOidcConfig(ctx, s.d.cfg.Shared, c.tenant); err != nil {
		return nil, rpcError(ctx, err)
	}
	_ = s.d.cfg.Router.Secrets().DeleteSecret(ctx, secrets.TenantSecretRef(c.tenant, ssoSecretKey))
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionDelete, map[string][2]any{"sso": {"configured", nil}})
	return &Empty{}, nil
}

// VerifySsoDomain looks for the ownership record and, if it is there, marks the domain verified.
// A record that is not there yet answers 409 so the page can say "not found yet, try again".
func (s *SsoAdminService) VerifySsoDomain(ctx context.Context, req *VerifySsoDomainRequest) (*SsoConfig, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	domain := strings.ToLower(strings.TrimSpace(req.GetDomain()))
	domains, err := sso.Domains(ctx, s.d.cfg.Shared, c.tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	var found *sso.SSODomain
	for i := range domains {
		if domains[i].Domain == domain {
			found = &domains[i]
		}
	}
	if found == nil {
		return nil, svcErr(ctx, http.StatusNotFound, errcode.NotFound, "That domain is not in this workspace's single sign-on settings.")
	}
	lookup := s.d.cfg.TXTLookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	ok, err := sso.CheckDomain(ctx, s.d.cfg.Shared, lookup, c.tenant, *found, s.d.now())
	switch {
	case errors.Is(err, sso.ErrDomainClaimed):
		return nil, svcErr(ctx, http.StatusConflict, errcode.UniqueViolation, "Another workspace has already verified this domain.")
	case err != nil:
		return nil, rpcError(ctx, err)
	case !ok:
		return nil, svcErr(ctx, http.StatusConflict, errcode.DomainNotVerified,
			"The record "+found.VerifyHost()+" was not found with the expected value yet. DNS changes can take a while; try again shortly.")
	}
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionUpdate, map[string][2]any{"verifiedDomain": {nil, domain}})
	return s.view(ctx, c.tenant)
}

// domainsView lists the tenant's domains with what each still needs (shared by both protocols).
func (s *SsoAdminService) domainsView(ctx context.Context, tenant uuid.UUID) ([]*SsoDomain, error) {
	domains, err := sso.Domains(ctx, s.d.cfg.Shared, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := make([]*SsoDomain, 0, len(domains))
	for _, d := range domains {
		status := "pending"
		if d.VerifiedAt != nil {
			status = "verified"
		}
		out = append(out, &SsoDomain{Domain: d.Domain, Status: status, VerifyHost: d.VerifyHost(), VerifyValue: d.Token})
	}
	return out, nil
}

func (s *SsoAdminService) samlView(ctx context.Context, tenant uuid.UUID) (*SamlConfig, error) {
	urls := sso.SamlURLsFor(s.d.cfg.PublicURL, tenant)
	out := &SamlConfig{EntityId: urls.EntityID, AcsUrl: urls.ACS, MetadataUrl: urls.Metadata, RoleMap: map[string]string{}, EmailDomains: []string{}}
	cfg, enabled, err := sso.StoredSamlConfig(ctx, s.d.cfg.Shared, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if cfg != nil {
		out.Configured, out.Enabled = true, enabled
		out.IdpMetadataUrl, out.EmailAttribute, out.RoleAttribute = cfg.IdpMetadataURL, cfg.EmailAttribute, cfg.RoleAttribute
		out.NameIdFormat = cfg.NameIDFormat
		if out.NameIdFormat == "" {
			out.NameIdFormat = sso.NameIDEmail
		}
		if cfg.RoleMap != nil {
			out.RoleMap = cfg.RoleMap
		}
		if cfg.EmailDomains != nil {
			out.EmailDomains = cfg.EmailDomains
		}
		out.DefaultRole, out.Enforced, out.PostLoginRedirect = cfg.DefaultRole, cfg.Enforced, cfg.PostLoginRedirect
		if md, err := sso.ParseIdpMetadata([]byte(cfg.IdpMetadataXML)); err == nil {
			out.IdpEntityId = md.EntityID
			out.IdpSsoUrl = md.RedirectURL
		}
	}
	if out.Domains, err = s.domainsView(ctx, tenant); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SsoAdminService) GetSamlConfig(ctx context.Context, _ *Empty) (*SamlConfig, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.samlView(ctx, c.tenant)
}

func (s *SsoAdminService) SetSamlConfig(ctx context.Context, req *SetSamlConfigRequest) (*SamlConfig, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	bad := func(msg string) (*SamlConfig, error) {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, msg)
	}
	if s.redirectURI() == "" {
		return nil, svcErr(ctx, http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available: this deployment has no public URL.")
	}
	access, err := s.validateAccess(ctx, c, accessInput{
		EmailDomains: req.EmailDomains, PostLoginRedirect: req.PostLoginRedirect, DefaultRole: req.DefaultRole,
		RoleClaim: req.RoleAttribute, RoleMap: req.RoleMap, Enabled: req.Enabled, Enforced: req.Enforced})
	if err != nil {
		return nil, err
	}
	if other, err := sso.OidcConfigFor(ctx, s.d.cfg.Shared, c.tenant); err != nil {
		return nil, rpcError(ctx, err)
	} else if other != nil && req.Enabled {
		return bad("OpenID Connect single sign-on is on for this workspace: turn it off before enabling SAML.")
	}

	before, _, _ := sso.StoredSamlConfig(ctx, s.d.cfg.Shared, c.tenant)
	format := strings.TrimSpace(req.NameIdFormat)
	if !sso.ValidNameIDFormat(format) {
		return bad("`nameIdFormat` is `emailAddress` or `persistent`.")
	}
	if format == sso.NameIDPersistent && strings.TrimSpace(req.EmailAttribute) == "" {
		return bad("With `persistent` NameIDs the address is not in the NameID: name the attribute that carries it in `emailAttribute`.")
	}
	cfg := &sso.SamlConfig{NameIDFormat: format, EmailAttribute: strings.TrimSpace(req.EmailAttribute), RoleAttribute: access.roleClaim, RoleMap: access.roleMap,
		EmailDomains: access.domains, DefaultRole: access.defaultRole, Enforced: req.Enforced, PostLoginRedirect: access.postLoginRedirect}
	switch {
	case strings.TrimSpace(req.IdpMetadataXml) != "":
		if len(req.IdpMetadataXml) > 1<<20 {
			return bad("The metadata is larger than 1 MB.")
		}
		if _, err := sso.ParseIdpMetadata([]byte(req.IdpMetadataXml)); err != nil {
			return bad("`idpMetadataXml` is not usable: " + err.Error() + ".")
		}
		cfg.IdpMetadataXML = req.IdpMetadataXml
	case strings.TrimSpace(req.IdpMetadataUrl) != "":
		xml, err := sso.FetchIdpMetadata(ctx, s.d.cfg.HTTPClient, strings.TrimSpace(req.IdpMetadataUrl))
		if err != nil {
			return bad("The identity provider's metadata could not be used: " + err.Error() + ".")
		}
		cfg.IdpMetadataXML, cfg.IdpMetadataURL = string(xml), strings.TrimSpace(req.IdpMetadataUrl)
	case before != nil:
		cfg.IdpMetadataXML, cfg.IdpMetadataURL = before.IdpMetadataXML, before.IdpMetadataURL
	default:
		return bad("Give the identity provider's metadata, as a URL or as XML.")
	}
	if err := sso.SaveSamlConfig(ctx, s.d.cfg.Shared, c.tenant, cfg, req.Enabled); err != nil {
		return nil, rpcError(ctx, err)
	}
	if err := sso.SyncDomains(ctx, s.d.cfg.Shared, c.tenant, access.domains); err != nil {
		return nil, rpcError(ctx, err)
	}
	diff := map[string][2]any{"saml": {nil, "configured"}, "enabled": {nil, req.Enabled}, "enforced": {nil, req.Enforced}}
	if before != nil {
		diff = map[string][2]any{"saml": {"configured", "updated"}, "enforced": {before.Enforced, req.Enforced}}
	}
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionUpdate, diff)
	return s.samlView(ctx, c.tenant)
}

func (s *SsoAdminService) DeleteSamlConfig(ctx context.Context, _ *Empty) (*Empty, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	// Removing an enforced configuration is how the owner (or an admin) restores password sign-in.
	if err := sso.DeleteSamlConfig(ctx, s.d.cfg.Shared, c.tenant); err != nil {
		return nil, rpcError(ctx, err)
	}
	s.d.recordFor(ctx, c, AuditEntitySSO, c.tenant, audit.ActionDelete, map[string][2]any{"saml": {"configured", nil}})
	return &Empty{}, nil
}
