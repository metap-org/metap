package admin

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/internal/admin/ssoconfig"
	"github.com/metap-org/metap/pkg/auth/sso"
)

// OwnerLookup is who owns a tenant, assigning the oldest active admin the first time anything asks
// (roadmap 115 M8). Slices that need the owner take it as a function so they do not import this package.
func OwnerLookup(cfg IdentityConfig) func(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	return identityDeps{cfg: cfg}.ensureOwner
}

// NewSsoService is the single sign-on slice wired to cfg's shared database, secret store and public URL.
func NewSsoService(cfg IdentityConfig) *ssoconfig.Service {
	deps := kernel.Deps{Cfg: cfg}
	var lookup sso.TXTLookup = cfg.TXTLookup
	return ssoconfig.New(ssoconfig.NewPgStore(deps, lookup), cfg.Router.Secrets(), ssoconfig.NetProvider{Client: cfg.HTTPClient},
		OwnerLookup(cfg), deps, cfg.PublicURL)
}
