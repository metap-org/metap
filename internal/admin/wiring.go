package admin

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/account"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/internal/admin/owner"
	"github.com/metap-org/metap/internal/admin/ssoconfig"
	"github.com/metap-org/metap/internal/admin/users"
	"github.com/metap-org/metap/pkg/auth/sso"
)

// The constructors below wire each slice to the deployment's shared database, secret store, mailer and
// public URL (an IdentityConfig). Slices do not import one another; what one needs of another is a
// small interface it takes in New, satisfied here.

// ownersOf is the owner slice as the others see it (users.Owners).
type ownersOf struct{ store owner.Store }

func (o ownersOf) Ensure(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	return owner.Ensure(ctx, o.store, tenant)
}

func (o ownersOf) Guard(ctx context.Context, c kernel.Caller, target uuid.UUID, what string) error {
	return owner.Guard(ctx, o.store, c, target, what)
}

// OwnerLookup is who owns a tenant, assigning the oldest active admin the first time anything asks
// (roadmap 115 M8).
func OwnerLookup(cfg IdentityConfig) func(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	return ownersOf{owner.NewPgStore(kernel.Deps{Cfg: cfg})}.Ensure
}

// NewUserService is user administration: members, roles, invitations, passwords, service accounts.
func NewUserService(cfg IdentityConfig) *users.Service {
	deps := kernel.Deps{Cfg: cfg}
	var notify users.Notifier
	if cfg.Mailer != nil {
		notify = cfg.Mailer
	}
	return users.New(users.NewPgStore(deps), ownersOf{owner.NewPgStore(deps)}, notify, deps)
}

// NewAccountService is the signed-in person's own view.
func NewAccountService(cfg IdentityConfig) *account.Service {
	deps := kernel.Deps{Cfg: cfg}
	return account.New(account.NewPgStore(deps), deps)
}

// NewSsoService is the single sign-on slice.
func NewSsoService(cfg IdentityConfig) *ssoconfig.Service {
	deps := kernel.Deps{Cfg: cfg}
	var lookup sso.TXTLookup = cfg.TXTLookup
	return ssoconfig.New(ssoconfig.NewPgStore(deps, lookup), cfg.Router.Secrets(), ssoconfig.NetProvider{Client: cfg.HTTPClient},
		OwnerLookup(cfg), deps, cfg.PublicURL)
}

// NewOwnerService is tenant ownership, recovery and workspace deletion.
func NewOwnerService(cfg IdentityConfig) *owner.Service {
	deps := kernel.Deps{Cfg: cfg}
	var notify owner.Notifier
	if cfg.Mailer != nil {
		notify = cfg.Mailer
	}
	return owner.New(owner.NewPgStore(deps), notify, deps)
}
