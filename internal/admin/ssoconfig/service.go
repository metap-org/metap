// Package ssoconfig is a tenant connecting its identity provider over OpenID Connect or SAML (roadmap
// 115 M7) as an administrative slice: types.go, rules.go (what a configuration may hold, the wire
// forms), store.go with its Postgres adapter, and this file. The provider is reached only through
// Provider, so saving a configuration can be tried without a network. Changing a configuration
// creates durable access, so a support session cannot (kernel.Deps.AdminDurable).
package ssoconfig

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/secrets"
)

type Service struct {
	store     Store
	secrets   Secrets
	provider  Provider
	ownerOf   OwnerOf
	deps      kernel.Deps
	publicURL string
}

// New is the service over store. publicURL is the address the identity provider sends people back to
// (empty: single sign-on is unavailable on this deployment).
func New(store Store, secrets Secrets, provider Provider, ownerOf OwnerOf, deps kernel.Deps, publicURL string) *Service {
	return &Service{store: store, secrets: secrets, provider: provider, ownerOf: ownerOf, deps: deps, publicURL: publicURL}
}

func (s *Service) redirectURI() string { return callbackURL(s.publicURL) }

func (s *Service) view(ctx context.Context, tenant uuid.UUID) (*SsoConfig, error) {
	cfg, enabled, err := s.store.Oidc(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return oidcView(s.redirectURI(), nil, false, false, nil), nil
	}
	_, secretErr := s.secrets.GetSecret(ctx, cfg.ClientSecretRef)
	domains, err := s.store.Domains(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return oidcView(s.redirectURI(), cfg, enabled, secretErr == nil, domains), nil
}

func (s *Service) samlView(ctx context.Context, tenant uuid.UUID) (*SamlConfig, error) {
	cfg, enabled, err := s.store.Saml(ctx, tenant)
	if err != nil {
		return nil, err
	}
	domains, err := s.store.Domains(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return samlView(s.publicURL, tenant, cfg, enabled, domains), nil
}

func (s *Service) GetOidc(ctx context.Context, _ *kernel.Empty) (*SsoConfig, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.Tenant)
}

func (s *Service) GetSaml(ctx context.Context, _ *kernel.Empty) (*SamlConfig, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.samlView(ctx, c.Tenant)
}

func unavailable() error {
	return kernel.Err(http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available: this deployment has no public URL.")
}

// checkAccess is the shape checks, then what needs the tenant: the roles the settings name must exist,
// and only the owner may require single sign-on (the owner keeps password sign-in as the way back).
func (s *Service) checkAccess(ctx context.Context, c kernel.Caller, in accessInput) (*accessSettings, error) {
	a, err := checkAccess(in, s.publicURL)
	if err != nil {
		return nil, err
	}
	if a.defaultRole != "" {
		if err := s.deps.CheckRoles(ctx, c.Tenant, []string{a.defaultRole}); err != nil {
			return nil, bad("`defaultRole` must be a role of this tenant.")
		}
	}
	for _, mapped := range a.roleMap {
		if err := s.deps.CheckRoles(ctx, c.Tenant, []string{mapped}); err != nil {
			return nil, bad("`roleMap` names a role this tenant does not have: " + mapped + ".")
		}
	}
	if in.Enforced {
		owner, err := s.ownerOf(ctx, c.Tenant)
		if err != nil {
			return nil, err
		}
		if owner == uuid.Nil || c.Self != owner {
			return nil, kernel.Err(http.StatusForbidden, errcode.OwnerProtected,
				"Only the tenant owner can require single sign-on: the owner keeps password sign-in as the way back.")
		}
	}
	return a, nil
}

func (s *Service) SetOidc(ctx context.Context, req *SetSsoConfigRequest) (*SsoConfig, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	if s.redirectURI() == "" {
		return nil, unavailable()
	}
	issuer := strings.TrimSpace(req.IssuerUrl)
	if err := checkIssuer(issuer); err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(req.ClientId)
	if clientID == "" || len(clientID) > 200 {
		return nil, bad("`clientId` is required.")
	}
	a, err := s.checkAccess(ctx, c, accessInput{
		EmailDomains: req.EmailDomains, PostLoginRedirect: req.PostLoginRedirect, DefaultRole: req.DefaultRole,
		RoleClaim: req.RoleClaim, RoleMap: req.RoleMap, Enabled: req.Enabled, Enforced: req.Enforced})
	if err != nil {
		return nil, err
	}
	if other, err := s.store.ActiveSaml(ctx, c.Tenant); err != nil {
		return nil, err
	} else if other != nil && req.Enabled {
		return nil, bad("SAML single sign-on is on for this workspace: turn it off before enabling OpenID Connect.")
	}

	ref := secrets.TenantSecretRef(c.Tenant, SecretKey)
	secret := req.ClientSecret
	if secret == "" {
		existing, err := s.secrets.GetSecret(ctx, ref)
		if err != nil {
			return nil, bad("`clientSecret` is required the first time.")
		}
		secret = existing
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	cfg := &sso.OidcConfig{IssuerURL: issuer, ClientID: clientID, ClientSecretRef: ref, RedirectURI: s.redirectURI(),
		Scopes: scopes, PostLoginRedirect: a.postLoginRedirect, EmailDomains: a.domains,
		DefaultRole: a.defaultRole, Enforced: req.Enforced, RoleClaim: a.roleClaim, RoleMap: a.roleMap}
	// Catch a wrong issuer now, not at someone's first sign-in.
	if err := s.provider.CheckOidc(ctx, cfg, secret); err != nil {
		return nil, bad("The identity provider could not be reached or is not an OpenID Connect issuer.")
	}
	before, _, _ := s.store.Oidc(ctx, c.Tenant)
	if req.ClientSecret != "" {
		if err := s.secrets.PutSecret(ctx, ref, req.ClientSecret); err != nil {
			return nil, kernel.Err(http.StatusConflict, errcode.Forbidden, err.Error())
		}
	}
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.SaveOidc(ctx, c.Tenant, cfg, req.Enabled, a.domains); err != nil {
			return nil, err
		}
		diff := map[string][2]any{"issuerUrl": {nil, issuer}, "enabled": {nil, req.Enabled}, "enforced": {nil, req.Enforced}}
		if before != nil {
			diff = map[string][2]any{"issuerUrl": {before.IssuerURL, issuer}, "enforced": {before.Enforced, req.Enforced}}
		}
		return diff, nil
	})
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.Tenant)
}

func (s *Service) DeleteOidc(ctx context.Context, _ *kernel.Empty) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	// Removing an enforced configuration is how the owner (or an admin) restores password sign-in.
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"sso": {"configured", nil}}, s.store.DeleteOidc(ctx, c.Tenant)
	})
	if err != nil {
		return nil, err
	}
	_ = s.secrets.DeleteSecret(ctx, secrets.TenantSecretRef(c.Tenant, SecretKey))
	return &kernel.Empty{}, nil
}

// VerifyDomain looks for the ownership record and, if it is there, marks the domain verified. A record
// that is not there yet answers 409 so the page can say "not found yet, try again".
func (s *Service) VerifyDomain(ctx context.Context, req *VerifySsoDomainRequest) (*SsoConfig, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	domains, err := s.store.Domains(ctx, c.Tenant)
	if err != nil {
		return nil, err
	}
	var found *sso.SSODomain
	for i := range domains {
		if domains[i].Domain == domain {
			found = &domains[i]
		}
	}
	if found == nil {
		return nil, kernel.Err(http.StatusNotFound, errcode.NotFound, "That domain is not in this workspace's single sign-on settings.")
	}
	notYet := kernel.Err(http.StatusConflict, errcode.DomainNotVerified,
		"The record "+found.VerifyHost()+" was not found with the expected value yet. DNS changes can take a while; try again shortly.")
	// A domain already proved stays proved: nothing changes and nothing is recorded.
	if found.VerifiedAt == nil {
		// DNS is asked before the transaction opens; the proof and its audit entry are then one change.
		if !s.store.DomainPublished(ctx, *found) {
			return nil, notYet
		}
		err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
			ok, err := s.store.MarkDomainVerified(ctx, c.Tenant, domain, s.deps.Now())
			switch {
			case errors.Is(err, sso.ErrDomainClaimed):
				return nil, kernel.Err(http.StatusConflict, errcode.UniqueViolation, "Another workspace has already verified this domain.")
			case err != nil:
				return nil, err
			case !ok:
				return nil, notYet
			}
			return map[string][2]any{"verifiedDomain": {nil, domain}}, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s.view(ctx, c.Tenant)
}

func (s *Service) SetSaml(ctx context.Context, req *SetSamlConfigRequest) (*SamlConfig, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	if s.redirectURI() == "" {
		return nil, unavailable()
	}
	a, err := s.checkAccess(ctx, c, accessInput{
		EmailDomains: req.EmailDomains, PostLoginRedirect: req.PostLoginRedirect, DefaultRole: req.DefaultRole,
		RoleClaim: req.RoleAttribute, RoleMap: req.RoleMap, Enabled: req.Enabled, Enforced: req.Enforced})
	if err != nil {
		return nil, err
	}
	if other, err := s.store.ActiveOidc(ctx, c.Tenant); err != nil {
		return nil, err
	} else if other != nil && req.Enabled {
		return nil, bad("OpenID Connect single sign-on is on for this workspace: turn it off before enabling SAML.")
	}

	before, _, _ := s.store.Saml(ctx, c.Tenant)
	format := strings.TrimSpace(req.NameIdFormat)
	if err := checkNameID(format, req.EmailAttribute); err != nil {
		return nil, err
	}
	cfg := &sso.SamlConfig{NameIDFormat: format, EmailAttribute: strings.TrimSpace(req.EmailAttribute), RoleAttribute: a.roleClaim, RoleMap: a.roleMap,
		EmailDomains: a.domains, DefaultRole: a.defaultRole, Enforced: req.Enforced, PostLoginRedirect: a.postLoginRedirect}
	switch {
	case strings.TrimSpace(req.IdpMetadataXml) != "":
		if len(req.IdpMetadataXml) > 1<<20 {
			return nil, bad("The metadata is larger than 1 MB.")
		}
		if _, err := sso.ParseIdpMetadata([]byte(req.IdpMetadataXml)); err != nil {
			return nil, bad("`idpMetadataXml` is not usable: " + err.Error() + ".")
		}
		cfg.IdpMetadataXML = req.IdpMetadataXml
	case strings.TrimSpace(req.IdpMetadataUrl) != "":
		url := strings.TrimSpace(req.IdpMetadataUrl)
		xml, err := s.provider.FetchSamlMetadata(ctx, url)
		if err != nil {
			return nil, bad("The identity provider's metadata could not be used: " + err.Error() + ".")
		}
		cfg.IdpMetadataXML, cfg.IdpMetadataURL = string(xml), url
	case before != nil:
		cfg.IdpMetadataXML, cfg.IdpMetadataURL = before.IdpMetadataXML, before.IdpMetadataURL
	default:
		return nil, bad("Give the identity provider's metadata, as a URL or as XML.")
	}
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.SaveSaml(ctx, c.Tenant, cfg, req.Enabled, a.domains); err != nil {
			return nil, err
		}
		diff := map[string][2]any{"saml": {nil, "configured"}, "enabled": {nil, req.Enabled}, "enforced": {nil, req.Enforced}}
		if before != nil {
			diff = map[string][2]any{"saml": {"configured", "updated"}, "enforced": {before.Enforced, req.Enforced}}
		}
		return diff, nil
	})
	if err != nil {
		return nil, err
	}
	return s.samlView(ctx, c.Tenant)
}

func (s *Service) DeleteSaml(ctx context.Context, _ *kernel.Empty) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	// Removing an enforced configuration is how the owner (or an admin) restores password sign-in.
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"saml": {"configured", nil}}, s.store.DeleteSaml(ctx, c.Tenant)
	})
	if err != nil {
		return nil, err
	}
	return &kernel.Empty{}, nil
}
