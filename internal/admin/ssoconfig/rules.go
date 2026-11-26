package ssoconfig

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntity is the audit-trail entity SSO configuration changes are recorded under (the record is
// the tenant).
const AuditEntity = kernel.AuditEntitySSO

// SecretKey is the config key the OIDC client secret is stored under; keep in step with httpapi.SSOSecretKey.
const SecretKey = "sso.clientSecret"

var emailDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$`)

func bad(msg string) error { return kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, msg) }

// sameOrigin is true when raw is an http(s) URL on base's scheme and host. Pure.
func sameOrigin(raw, base string) bool {
	u, err := url.Parse(raw)
	b, err2 := url.Parse(base)
	if err != nil || err2 != nil || u.Host == "" || b.Host == "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == b.Scheme && strings.EqualFold(u.Host, b.Host)
}

// callbackURL is where the identity provider sends people back ("" without a public URL). Pure.
func callbackURL(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/auth/sso/callback"
}

// checkIssuer is the 400 for an issuer that is not https (plain http is accepted only for loopback,
// local development providers). Pure.
func checkIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return bad("`issuerUrl` must be an https URL.")
	}
	return nil
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

// roles is every role the settings name: the ones that must exist in the tenant.
func (a *accessSettings) roles() []string {
	var out []string
	if a.defaultRole != "" {
		out = append(out, a.defaultRole)
	}
	for _, mapped := range a.roleMap {
		out = append(out, mapped)
	}
	return out
}

// checkAccess checks and normalises the shape of the shared settings; every refusal is a 400 with the
// reason. That the named roles exist, and who may enforce, need the tenant and are the service's. Pure.
func checkAccess(in accessInput, publicURL string) (*accessSettings, error) {
	var domains []string
	for _, d := range in.EmailDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !emailDomainPattern.MatchString(d) {
			return nil, bad("`" + d + "` is not an email domain.")
		}
		domains = append(domains, d)
	}
	if len(domains) > 20 {
		return nil, bad("At most 20 email domains.")
	}
	// The landing page must be on the service's own origin: the callback redirects there with a fresh
	// session cookie, so it cannot point anywhere else.
	redirect := strings.TrimSpace(in.PostLoginRedirect)
	if redirect != "" && !sameOrigin(redirect, publicURL) {
		return nil, bad("`postLoginRedirect` must be a URL on " + publicURL + ".")
	}
	role := strings.TrimSpace(in.DefaultRole)
	if role == permission.RoleAdmin {
		return nil, bad("People provisioned by single sign-on cannot start as admin.")
	}
	roleClaim := strings.TrimSpace(in.RoleClaim)
	roleMap := map[string]string{}
	for value, mapped := range in.RoleMap {
		value, mapped = strings.TrimSpace(value), strings.TrimSpace(mapped)
		if value == "" || mapped == "" {
			return nil, bad("`roleMap` needs a provider value and a role on every line.")
		}
		roleMap[value] = mapped
	}
	if len(roleMap) > 50 {
		return nil, bad("At most 50 role mappings.")
	}
	if len(roleMap) > 0 && roleClaim == "" {
		return nil, bad("`roleClaim` is required when `roleMap` is set.")
	}
	for _, mapped := range roleMap {
		// Like `defaultRole`: the provider never decides who administers the workspace.
		if mapped == permission.RoleAdmin || mapped == access.RolePlatformAdmin {
			return nil, bad("A provider group cannot be mapped to " + mapped + ".")
		}
	}
	if in.Enforced && !in.Enabled {
		return nil, bad("Single sign-on must be enabled to be enforced.")
	}
	return &accessSettings{domains: domains, postLoginRedirect: redirect, defaultRole: role, roleClaim: roleClaim, roleMap: roleMap}, nil
}

// domainsView lists the tenant's domains with what each still needs (shared by both protocols). Pure.
func domainsView(domains []sso.SSODomain) []*SsoDomain {
	out := make([]*SsoDomain, 0, len(domains))
	for _, d := range domains {
		status := "pending"
		if d.VerifiedAt != nil {
			status = "verified"
		}
		out = append(out, &SsoDomain{Domain: d.Domain, Status: status, VerifyHost: d.VerifyHost(), VerifyValue: d.Token})
	}
	return out
}

// oidcView is the OIDC settings as the API shows them (cfg nil: not configured). Pure.
func oidcView(redirectURI string, cfg *sso.OidcConfig, enabled, hasSecret bool, domains []sso.SSODomain) *SsoConfig {
	out := &SsoConfig{RedirectUri: redirectURI}
	if cfg == nil {
		return out
	}
	out.Configured, out.Enabled = true, enabled
	out.IssuerUrl, out.ClientId, out.HasClientSecret = cfg.IssuerURL, cfg.ClientID, hasSecret
	out.Scopes, out.EmailDomains, out.DefaultRole = cfg.Scopes, cfg.EmailDomains, cfg.DefaultRole
	out.Enforced, out.PostLoginRedirect = cfg.Enforced, cfg.PostLoginRedirect
	out.RoleClaim, out.RoleMap = cfg.RoleClaim, cfg.RoleMap
	out.Domains = domainsView(domains)
	if out.RoleMap == nil {
		out.RoleMap = map[string]string{}
	}
	return out
}

// samlView is the SAML settings as the API shows them (cfg nil: not configured). Pure.
func samlView(publicURL string, tenant uuid.UUID, cfg *sso.SamlConfig, enabled bool, domains []sso.SSODomain) *SamlConfig {
	urls := sso.SamlURLsFor(publicURL, tenant)
	out := &SamlConfig{EntityId: urls.EntityID, AcsUrl: urls.ACS, MetadataUrl: urls.Metadata, RoleMap: map[string]string{}, EmailDomains: []string{}}
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
	out.Domains = domainsView(domains)
	return out
}

// checkNameID is the 400 for a NameID format SAML sign-in does not know, or for `persistent` without
// the attribute that carries the address. Pure.
func checkNameID(format, emailAttribute string) error {
	if !sso.ValidNameIDFormat(format) {
		return bad("`nameIdFormat` is `emailAddress` or `persistent`.")
	}
	if format == sso.NameIDPersistent && strings.TrimSpace(emailAttribute) == "" {
		return bad("With `persistent` NameIDs the address is not in the NameID: name the attribute that carries it in `emailAttribute`.")
	}
	return nil
}
