package kernel

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Audit entities: the names administrative changes are recorded under, so a trail reads the same
// whichever slice made the change.
const (
	AuditEntityUsers          = "metadata.users"
	AuditEntityRoles          = "metadata.roles"
	AuditEntityPolicies       = "metadata.policies"
	AuditEntityOwner          = "metadata.tenant_owner"
	AuditEntitySSO            = "metadata.sso_config"
	AuditEntityCredentials    = "metadata.tenant_credentials"
	AuditEntityConfig         = "metadata.tenant_config"
	AuditEntityPlatformConfig = "metadata.platform_config"
)

// CredentialCapabilities is where this deployment can keep a credential's value.
type CredentialCapabilities struct {
	Database     bool
	External     bool
	ExternalKind string // "vault", "aws", "gcp"; "env" when none is configured
}

// CredentialManager is the store behind the Credentials tab (implemented over secrets.Manager).
// ref is always secrets.TenantCredentialRef, derived from the credential's own row, never supplied.
type CredentialManager interface {
	Capabilities() CredentialCapabilities
	Put(ctx context.Context, ref, source, value string) error
	Remove(ctx context.Context, ref string) error
	// Get reads a stored value for the service itself (changing a userpass's username keeps its
	// password); it is never sent to a client.
	Get(ctx context.Context, ref string) (string, error)
}

// Config is what the administrative services are built from.
type Config struct {
	Shared *pgxpool.Pool
	Router *tenancy.Router
	// Audit records every administrative change; nil records nothing.
	Audit audit.Store
	// Sink, when set, makes an administrative change and its audit entry one transaction on the shared
	// database (Change); nil keeps "change, then record best-effort through Audit".
	Sink action.Sink
	// PublicURL is the origin people reach the service at; the SSO redirect URI is built from it.
	PublicURL string
	// Mailer sends invitation emails; nil sends none (the admin gets the link back instead).
	Mailer *mail.Mailer
	// PasswordPolicy is each tenant's minimum password length; nil means the platform minimum.
	PasswordPolicy identity.PasswordPolicy
	// APIKeyMaxDays is the tenant's cap on an API key's lifetime in days (auth.apiKeyMaxLifetimeDays);
	// nil or 0 means no tenant cap.
	APIKeyMaxDays func(ctx context.Context, tenant uuid.UUID) int
	// Credentials stores the tenant credentials the Credentials tab manages; nil serves none.
	Credentials CredentialManager
	// HTTPClient fetches a SAML provider's metadata from the URL an admin gives (nil: a plain client
	// with a timeout); a deployment can put its outbound-request guard here.
	HTTPClient *http.Client
	// TXTLookup resolves the DNS record that proves a domain is owned; nil uses the system resolver.
	TXTLookup sso.TXTLookup
}

// Deps is a service's view of the Config: the clock, who is calling, and the one way to make a
// change and record it.
type Deps struct {
	Cfg    Config
	NowFor func() time.Time
}

// Now is the clock (NowFor, else time.Now).
func (d Deps) Now() time.Time {
	if d.NowFor != nil {
		return d.NowFor()
	}
	return time.Now()
}

// Caller is the authenticated caller in ctx.
func (d Deps) Caller(ctx context.Context) (Caller, error) { return Authenticated(ctx) }

// Admin is the caller, who must hold the admin role (and the admin OAuth2 scope).
func (d Deps) Admin(ctx context.Context) (Caller, error) { return At(ctx, access.Admin) }

// At is the caller, who must pass level.
func (d Deps) At(ctx context.Context, level access.Level) (Caller, error) { return At(ctx, level) }

// AdminDurable is Admin for an action that creates access outliving the moment: a person, a service
// account, a key, a role, a password or MFA reset, a sign-in provider. A support session (vendor
// access, time-boxed by a grant) is refused, so revoking or lapsing the grant really ends it.
func (d Deps) AdminDurable(ctx context.Context) (Caller, error) {
	return Durable(ctx, func(ctx context.Context, tenant, member uuid.UUID) (bool, error) {
		return identity.IsSupportSessionMember(ctx, d.Cfg.Shared, tenant, member)
	})
}

// SupportSessionRefusal says why rc may not use an access.Durable operation because it is a
// vendor's support session ("" when it may). It needs the database, so it is not part of
// access.Refusal; the route gate and AdminDurable both ask it.
func SupportSessionRefusal(ctx context.Context, cfg Config, rc *permission.RequestContext) (string, error) {
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return "", nil
	}
	self, err := uuid.Parse(rc.UserID)
	if err != nil || self == uuid.Nil {
		return "", nil
	}
	support, err := identity.IsSupportSessionMember(ctx, cfg.Shared, tenant, self)
	if err != nil {
		return "", err
	}
	if support {
		return SupportRefusal, nil
	}
	return "", nil
}

// WithTenantTx runs fn in a transaction on tenant's own database (where user_roles live).
func (d Deps) WithTenantTx(ctx context.Context, tenant uuid.UUID, fn func(db rolestore.DBTX) error) error {
	tx, err := d.Cfg.Router.Begin(ctx, tenant)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// PlatformAdmin is the caller, who must be a platform_admin of the platform tenant (and hold the
// admin OAuth2 scope when the token is scoped).
func (d Deps) PlatformAdmin(ctx context.Context) (Caller, error) {
	return At(ctx, access.PlatformAdmin)
}

// KnownRoles is what a member may be given: the tenant's role catalog (roadmap 114), whose built-in
// rows are created on first use. A name outside it is a typo, not a role.
func (d Deps) KnownRoles(ctx context.Context, tenant uuid.UUID) ([]string, error) {
	var roles []string
	err := d.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		var err error
		roles, err = rolestore.RoleNames(ctx, db, tenant)
		return err
	})
	return roles, err
}

// CheckRoles is nil when every role is in the tenant's catalog, else the 400 that names the first
// that is not.
func (d Deps) CheckRoles(ctx context.Context, tenant uuid.UUID, roles []string) error {
	known, err := d.KnownRoles(ctx, tenant)
	if err != nil {
		return err
	}
	return UnknownRoles(known, roles)
}

// UnknownRoles is nil when every role is in known, else the 400 naming the first that is not. Pure.
func UnknownRoles(known, roles []string) error {
	for _, r := range roles {
		if !slices.Contains(known, r) {
			return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
				Message: "Unknown role " + r + ".", FieldErrors: map[string][]string{"roles": {"unknown role " + r}}}
		}
	}
	return nil
}

// TenantErr maps the tenant registry's errors (and identity's) onto the platform envelope.
func TenantErr(err error) error {
	switch {
	case errors.Is(err, tenancy.ErrTenantNotFound):
		return Err(http.StatusNotFound, errcode.NotFound, "Tenant not found.")
	case errors.Is(err, tenancy.ErrTenantState):
		return Err(http.StatusConflict, errcode.TenantInvalidState, "The tenant is not in a state that allows this.")
	}
	return IdentityErr(err)
}

// DB is the shared identity database as the statements of pkg/auth/identity take it: each runs on the
// transaction of the unit of work in its context when there is one on the shared database, else on
// the pool, so it can sit inside a change without asking the pool for a second connection.
func (d Deps) DB() IdentityDB { return IdentityDB{Pool: d.Cfg.Shared} }

// Auditor is the older best-effort record, the no-Sink path of Change.
func (d Deps) Auditor() Auditor { return deps{d} }

type deps struct{ d Deps }

func (a deps) Record(ctx context.Context, c Caller, entity string, id uuid.UUID, act audit.AuditAction, diff map[string][2]any) {
	WriteAudit(ctx, a.d.Cfg.Audit, c.Tenant, c.Self, entity, id, act, diff, a.d.Now())
}

// Change runs one administrative change and records it as one transaction when a Sink is configured.
// Nothing inside fn may open the tenant's own database: it would take a second connection while the
// change holds the first, so work on user_roles goes before or after.
func (d Deps) Change(ctx context.Context, c Caller, entity string, id uuid.UUID, act audit.AuditAction,
	fn func(ctx context.Context) (map[string][2]any, error)) error {
	return Change(ctx, d.Cfg.Sink, d.Auditor(), d.NowFor, c, entity, id, act, fn)
}

// ChangeNew is Change for a change that creates the thing it audits: fn returns its id.
func (d Deps) ChangeNew(ctx context.Context, c Caller, entity string, act audit.AuditAction,
	fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	return ChangeNew(ctx, d.Cfg.Sink, d.Auditor(), d.NowFor, c, entity, act, fn)
}

// ChangeInTenant is Change for work on the tenant's own database (its roles, its policies): the work
// and the entry are one transaction there, and what opens a transaction through the router joins it.
func (d Deps) ChangeInTenant(ctx context.Context, c Caller, entity string, id uuid.UUID, act audit.AuditAction,
	fn func(ctx context.Context) (map[string][2]any, error)) error {
	return ChangeInTenant(ctx, d.Cfg.Sink, d.Auditor(), d.NowFor, c, entity, id, act, fn)
}

// ChangeInTenantNew is ChangeInTenant for a change that creates the thing it audits.
func (d Deps) ChangeInTenantNew(ctx context.Context, c Caller, entity string, act audit.AuditAction,
	fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	return ChangeInTenantNew(ctx, d.Cfg.Sink, d.Auditor(), d.NowFor, c, entity, act, fn)
}

// RecordFor writes one audit entry for a change that has already been made (the no-Sink path).
func (d Deps) RecordFor(ctx context.Context, c Caller, entity string, id uuid.UUID, act audit.AuditAction, diff map[string][2]any) {
	WriteAudit(ctx, d.Cfg.Audit, c.Tenant, c.Self, entity, id, act, diff, d.Now())
}
