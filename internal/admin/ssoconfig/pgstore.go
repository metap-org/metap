package ssoconfig

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/sso"
)

// PgStore keeps the settings in tenant_auth_configs and sso_domains.
type PgStore struct {
	deps   kernel.Deps
	lookup sso.TXTLookup
}

// NewPgStore is the store over the shared database. lookup resolves the TXT record that proves a
// domain (nil: the system resolver).
func NewPgStore(deps kernel.Deps, lookup sso.TXTLookup) *PgStore {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	return &PgStore{deps: deps, lookup: lookup}
}

func (s *PgStore) db() kernel.IdentityDB { return s.deps.DB() }

func (s *PgStore) Oidc(ctx context.Context, t uuid.UUID) (*sso.OidcConfig, bool, error) {
	return sso.StoredOidcConfig(ctx, s.db(), t)
}
func (s *PgStore) Saml(ctx context.Context, t uuid.UUID) (*sso.SamlConfig, bool, error) {
	return sso.StoredSamlConfig(ctx, s.db(), t)
}
func (s *PgStore) ActiveOidc(ctx context.Context, t uuid.UUID) (*sso.OidcConfig, error) {
	return sso.OidcConfigFor(ctx, s.db(), t)
}
func (s *PgStore) ActiveSaml(ctx context.Context, t uuid.UUID) (*sso.SamlConfig, error) {
	return sso.SamlConfigFor(ctx, s.db(), t)
}

func (s *PgStore) SaveOidc(ctx context.Context, t uuid.UUID, cfg *sso.OidcConfig, enabled bool, domains []string) error {
	if err := sso.SaveOidcConfig(ctx, s.db(), t, cfg, enabled); err != nil {
		return err
	}
	return sso.SyncDomains(ctx, s.db(), t, domains)
}

func (s *PgStore) SaveSaml(ctx context.Context, t uuid.UUID, cfg *sso.SamlConfig, enabled bool, domains []string) error {
	if err := sso.SaveSamlConfig(ctx, s.db(), t, cfg, enabled); err != nil {
		return err
	}
	return sso.SyncDomains(ctx, s.db(), t, domains)
}

func (s *PgStore) DeleteOidc(ctx context.Context, t uuid.UUID) error {
	return sso.DeleteOidcConfig(ctx, s.db(), t)
}
func (s *PgStore) DeleteSaml(ctx context.Context, t uuid.UUID) error {
	return sso.DeleteSamlConfig(ctx, s.db(), t)
}

func (s *PgStore) Domains(ctx context.Context, t uuid.UUID) ([]sso.SSODomain, error) {
	return sso.Domains(ctx, s.db(), t)
}

func (s *PgStore) DomainPublished(ctx context.Context, d sso.SSODomain) bool {
	return sso.DomainRecordPublished(ctx, s.lookup, d)
}

func (s *PgStore) MarkDomainVerified(ctx context.Context, t uuid.UUID, domain string, now time.Time) (bool, error) {
	return sso.MarkDomainVerified(ctx, s.db(), t, domain, now)
}

// NetProvider reaches the identity provider over the network.
type NetProvider struct{ Client *http.Client }

func (p NetProvider) CheckOidc(ctx context.Context, cfg *sso.OidcConfig, secret string) error {
	_, _, _, _, err := sso.OidcAuthorizeURL(ctx, cfg, secret)
	return err
}

func (p NetProvider) FetchSamlMetadata(ctx context.Context, url string) ([]byte, error) {
	return sso.FetchIdpMetadata(ctx, p.Client, url)
}
