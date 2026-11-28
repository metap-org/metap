package ssoconfig

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/sso"
)

// Store is a tenant's single sign-on settings on the shared database: the OIDC and SAML
// configuration, and the email domains they claim. Writes run on the transaction of the unit of work
// in ctx when there is one (kernel.IdentityDB), so a save and its audit entry are one transaction.
type Store interface {
	// Oidc/Saml are what is saved with whether it is switched on (nil when nothing is saved);
	// ActiveOidc/ActiveSaml are nil unless it is on.
	Oidc(ctx context.Context, tenant uuid.UUID) (*sso.OidcConfig, bool, error)
	Saml(ctx context.Context, tenant uuid.UUID) (*sso.SamlConfig, bool, error)
	ActiveOidc(ctx context.Context, tenant uuid.UUID) (*sso.OidcConfig, error)
	ActiveSaml(ctx context.Context, tenant uuid.UUID) (*sso.SamlConfig, error)

	SaveOidc(ctx context.Context, tenant uuid.UUID, cfg *sso.OidcConfig, enabled bool, domains []string) error
	SaveSaml(ctx context.Context, tenant uuid.UUID, cfg *sso.SamlConfig, enabled bool, domains []string) error
	DeleteOidc(ctx context.Context, tenant uuid.UUID) error
	DeleteSaml(ctx context.Context, tenant uuid.UUID) error

	Domains(ctx context.Context, tenant uuid.UUID) ([]sso.SSODomain, error)
	// DomainPublished looks up the ownership record (network only, so outside any transaction).
	DomainPublished(ctx context.Context, d sso.SSODomain) bool
	// MarkDomainVerified records the proof; false when the tenant's settings no longer name the domain.
	MarkDomainVerified(ctx context.Context, tenant uuid.UUID, domain string, now time.Time) (bool, error)
}

// Secrets keeps the OIDC client secret (the deployment's secret store).
type Secrets interface {
	GetSecret(ctx context.Context, ref string) (string, error)
	PutSecret(ctx context.Context, ref, value string) error
	DeleteSecret(ctx context.Context, ref string) error
}

// Provider reaches the identity provider: what saving a configuration checks before it is trusted.
type Provider interface {
	// CheckOidc is nil when issuer is an OpenID Connect provider that answers.
	CheckOidc(ctx context.Context, cfg *sso.OidcConfig, secret string) error
	// FetchSamlMetadata is the provider's SAML metadata at url.
	FetchSamlMetadata(ctx context.Context, url string) ([]byte, error)
}

// OwnerOf is who owns the tenant (uuid.Nil: nobody yet). Requiring SSO is the owner's call alone.
type OwnerOf func(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error)
