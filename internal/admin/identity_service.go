// UserAdminService and AccountService (roadmap 113): user administration for a tenant's admins
// and the caller's own account, over pkg/identity. Members live in the shared database
// (IdentityConfig.Shared); roles in each tenant's own, through Router. Served only by the
// token-issuing service, which owns identities.
package admin

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// AuditEntityUsers is the audit-trail entity name user administration records under.
const AuditEntityUsers = "metadata.users"

// RolePlatformAdmin is the built-in role of the platform tenant.
const RolePlatformAdmin = access.RolePlatformAdmin

// IdentityConfig turns on UserAdminService and AccountService on a gRPC listener.
type IdentityConfig struct {
	Shared *pgxpool.Pool
	Router *tenancy.Router
	// Audit records every administrative change; nil records nothing.
	Audit audit.Store
	// Sink, when set, makes an administrative change and its audit entry one transaction on the shared
	// database (kernel.Change); nil keeps "change, then record best-effort through Audit".
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

type identityDeps struct {
	cfg    IdentityConfig
	nowFor func() time.Time
}

func (d identityDeps) now() time.Time {
	if d.nowFor != nil {
		return d.nowFor()
	}
	return time.Now()
}

// UserAdminService manages the members of the caller's tenant. Every call needs the admin role.
type UserAdminService struct {
	d identityDeps
}

// AccountService is the caller's own account.
type AccountService struct {
	d identityDeps
}

func NewUserAdminService(cfg IdentityConfig) *UserAdminService {
	return &UserAdminService{d: identityDeps{cfg: cfg}}
}

func NewAccountService(cfg IdentityConfig) *AccountService {
	return &AccountService{d: identityDeps{cfg: cfg}}
}

// caller is an authenticated request's identity: tenant, own member id (uuid.Nil for a
// non-person subject) and the request context.
type caller struct {
	rc     *permission.RequestContext
	tenant uuid.UUID
	self   uuid.UUID
}

func (d identityDeps) caller(ctx context.Context) (caller, error) {
	rc, err := resolveCaller(ctx)
	if err != nil {
		return caller{}, err
	}
	if rc == nil {
		return caller{}, svcErr(ctx, http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid authorization header.")
	}
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return caller{}, svcErr(ctx, http.StatusUnauthorized, errcode.Unauthorized, "token is missing required claims")
	}
	self, _ := uuid.Parse(rc.UserID)
	return caller{rc: rc, tenant: tenant, self: self}, nil
}

func (s *UserAdminService) admin(ctx context.Context) (caller, error) { return s.d.admin(ctx) }

// admin authenticates a caller that must hold the admin role (and the admin OAuth2 scope).
func (d identityDeps) admin(ctx context.Context) (caller, error) {
	return d.at(ctx, access.Admin)
}

// at authenticates a caller and checks that level lets them in.
func (d identityDeps) at(ctx context.Context, level access.Level) (caller, error) {
	c, err := d.caller(ctx)
	if err != nil {
		return caller{}, err
	}
	if why := access.Refusal(level, c.rc); why != "" {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, why)
	}
	return c, nil
}

// adminDurable is admin for an action that creates access outliving the moment: a person, a service
// account, a key, a role, a password or MFA reset, a sign-in provider. A support session (vendor
// access, time-boxed by a grant) is refused, so revoking or lapsing the grant really ends it; the
// tenant's own admins make these changes.
func adminDurable(d identityDeps, ctx context.Context) (caller, error) {
	c, err := d.at(ctx, access.Durable)
	if err != nil {
		return caller{}, err
	}
	why, err := SupportSessionRefusal(ctx, d.cfg, c.rc)
	if err != nil {
		return caller{}, rpcError(ctx, err)
	}
	if why != "" {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, why)
	}
	return c, nil
}

// SupportSessionRefusal says why rc may not use an access.Durable operation because it is a
// vendor's support session ("" when it may). It needs the database, so it is not part of
// access.Refusal; the route gate and adminDurable both ask it.
func SupportSessionRefusal(ctx context.Context, cfg IdentityConfig, rc *permission.RequestContext) (string, error) {
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
		return kernel.SupportRefusal, nil
	}
	return "", nil
}

func svcErr(ctx context.Context, httpStatus uint16, code, message string) error {
	return sendServiceError(ctx, crud.NewServiceErrorMsg(httpStatus, code, message))
}

// identityErr maps pkg/identity's errors onto the platform error envelope.
func identityErr(_ context.Context, err error) error { return kernel.IdentityErr(err) }

// withTenantTx runs fn in a transaction on tenant's own database (where user_roles live).
func (d identityDeps) withTenantTx(ctx context.Context, tenant uuid.UUID, fn func(db rolestore.DBTX) error) error {
	tx, err := d.cfg.Router.Begin(ctx, tenant)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// toUsers converts members, attaching their roles and the shared-identity flag.
func (d identityDeps) toUsers(ctx context.Context, tenant uuid.UUID, ms []identity.Member) ([]*User, error) {
	ids := make([]uuid.UUID, len(ms))
	identities := make([]uuid.UUID, len(ms))
	for i, m := range ms {
		ids[i], identities[i] = m.ID, m.IdentityID
	}
	var roles map[uuid.UUID][]string
	var scoped map[uuid.UUID][]permission.ScopedRole
	if err := d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if roles, err = rolestore.RolesForUsers(ctx, db, tenant, ids); err != nil {
			return err
		}
		scoped, err = rolestore.ScopedRolesForUsers(ctx, db, tenant, ids)
		return err
	}); err != nil {
		return nil, err
	}
	shared, err := identity.SharedIdentities(ctx, d.db(), tenant, identities)
	if err != nil {
		return nil, err
	}
	owner, err := d.ensureOwner(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]*User, len(ms))
	for i, m := range ms {
		u := &User{IsOwner: m.ID == owner,
			Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName, Status: m.Status,
			Roles: roles[m.ID], AuthProvider: m.AuthProvider, HasPassword: m.HasPassword,
			LastLoginAt: formatTime(m.LastLoginAt), CreatedAt: formatTime(&m.CreatedAt),
			SharedIdentity: shared[m.IdentityID],
		}
		if u.Roles == nil {
			u.Roles = []string{}
		}
		for _, sr := range scoped[m.ID] {
			u.ScopedRoles = append(u.ScopedRoles, &ScopedRole{Role: sr.Role, ScopeType: sr.ScopeType, ScopeId: sr.ScopeID})
		}
		if m.InvitedBy != nil {
			u.InvitedBy = m.InvitedBy.String()
		}
		if m.Status == identity.StatusInvited {
			if inv, err := identity.PendingInvitation(ctx, d.db(), m.ID, d.now()); err == nil && inv != nil {
				u.InvitationExpiresAt = formatTime(&inv.ExpiresAt)
			}
		}
		out[i] = u
	}
	return out, nil
}

func (d identityDeps) user(ctx context.Context, tenant, id uuid.UUID) (*User, error) {
	m, err := identity.GetMember(ctx, d.db(), tenant, id)
	if err != nil {
		return nil, err
	}
	users, err := d.toUsers(ctx, tenant, []identity.Member{*m})
	if err != nil {
		return nil, err
	}
	return users[0], nil
}

// record writes one audit entry for an administrative change to member id. Best-effort, like the
// CRUD layer's: the change itself already committed.
func (d identityDeps) record(ctx context.Context, c caller, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	d.recordFor(ctx, c, AuditEntityUsers, id, action, diff)
}

// db is the shared identity database as the statements of pkg/auth/identity take it: each runs on the
// transaction of the unit of work in its context when there is one on the shared database, else on
// the pool, so it can sit inside a change without asking the pool for a second connection.
func (d identityDeps) db() kernel.IdentityDB { return kernel.IdentityDB{Pool: d.cfg.Shared} }

// change runs one administrative change and records it as one transaction when a Sink is configured
// (kernel.Change). Nothing inside fn may open the tenant's own database: it would take a second
// connection while the change holds the first, so work on user_roles goes before or after.
func (d identityDeps) change(ctx context.Context, c caller, entity string, id uuid.UUID, act audit.AuditAction,
	fn func(ctx context.Context) (map[string][2]any, error)) error {
	kc := kernel.Caller{RC: c.rc, Tenant: c.tenant, Self: c.self}
	return kernel.Change(ctx, d.cfg.Sink, adminAuditor{d}, d.nowFor, kc, entity, id, act, fn)
}

// changeNew is change for a change that creates the thing it audits: fn returns its id.
func (d identityDeps) changeNew(ctx context.Context, c caller, entity string, act audit.AuditAction,
	fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	kc := kernel.Caller{RC: c.rc, Tenant: c.tenant, Self: c.self}
	return kernel.ChangeNew(ctx, d.cfg.Sink, adminAuditor{d}, d.nowFor, kc, entity, act, fn)
}

// changeInTenant is change for work on the tenant's own database (its roles, its policies): the work
// and the entry are one transaction there, and what opens a transaction through the router joins it.
func (d identityDeps) changeInTenant(ctx context.Context, c caller, entity string, id uuid.UUID, act audit.AuditAction,
	fn func(ctx context.Context) (map[string][2]any, error)) error {
	kc := kernel.Caller{RC: c.rc, Tenant: c.tenant, Self: c.self}
	return kernel.ChangeInTenant(ctx, d.cfg.Sink, adminAuditor{d}, d.nowFor, kc, entity, id, act, fn)
}

// changeInTenantNew is changeInTenant for a change that creates the thing it audits.
func (d identityDeps) changeInTenantNew(ctx context.Context, c caller, entity string, act audit.AuditAction,
	fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	kc := kernel.Caller{RC: c.rc, Tenant: c.tenant, Self: c.self}
	return kernel.ChangeInTenantNew(ctx, d.cfg.Sink, adminAuditor{d}, d.nowFor, kc, entity, act, fn)
}

// adminAuditor is the older best-effort record, for the no-Sink path of change.
type adminAuditor struct{ d identityDeps }

func (a adminAuditor) Record(ctx context.Context, c kernel.Caller, entity string, id uuid.UUID, act audit.AuditAction, diff map[string][2]any) {
	a.d.recordFor(ctx, caller{rc: c.RC, tenant: c.Tenant, self: c.Self}, entity, id, act, diff)
}

// recordFor is record for any administered entity (metadata.users, metadata.roles).
func (d identityDeps) recordFor(ctx context.Context, c caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	writeAdminAudit(ctx, d.cfg.Audit, c.tenant, c.self, entity, id, action, diff, d.now())
}

func parseID(id string) (uuid.UUID, error) { return kernel.ParseID(id) }

func (s *UserAdminService) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	q := identity.ListQuery{Search: req.GetSearch(), Status: req.GetStatus(), Offset: int(req.GetOffset()), Limit: int(req.GetLimit())}
	if role := strings.TrimSpace(req.GetRole()); role != "" {
		if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
			q.IDs, err = rolestore.UsersWithRole(ctx, db, c.tenant, role)
			return err
		}); err != nil {
			return nil, rpcError(ctx, err)
		}
	}
	ms, total, err := identity.ListMembers(ctx, s.d.db(), c.tenant, q)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	users, err := s.d.toUsers(ctx, c.tenant, ms)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	return &ListUsersResponse{Users: users, Total: int32(total)}, nil
}

func (s *UserAdminService) GetUser(ctx context.Context, req *UserIdRequest) (*User, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	u, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return u, nil
}

// knownRoles is what a member may be given: the tenant's role catalog (roadmap 114), whose
// built-in rows are created on first use. A name outside it is a typo, not a role.
func (d identityDeps) knownRoles(ctx context.Context, tenant uuid.UUID) ([]string, error) {
	var roles []string
	err := d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		var err error
		roles, err = rolestore.RoleNames(ctx, db, tenant)
		return err
	})
	return roles, err
}

func (d identityDeps) checkRoles(ctx context.Context, tenant uuid.UUID, roles []string) error {
	known, err := d.knownRoles(ctx, tenant)
	if err != nil {
		return rpcError(ctx, err)
	}
	for _, r := range roles {
		if !slices.Contains(known, r) {
			return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
				Message: "Unknown role " + r + ".", FieldErrors: map[string][]string{"roles": {"unknown role " + r}}})
		}
	}
	return nil
}

func (s *UserAdminService) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	roles := dedupe(req.GetRoles())
	if err := s.d.checkRoles(ctx, c.tenant, roles); err != nil {
		return nil, err
	}
	if req.GetPassword() != "" {
		// The password only applies to a new account, which belongs to this tenant alone.
		if err := identity.CheckPasswordPolicy(ctx, s.d.db(), uuid.Nil, req.GetPassword(), s.d.cfg.PasswordPolicy, c.tenant); err != nil {
			return nil, identityErr(ctx, err)
		}
	}
	invitedBy := &c.self
	if c.self == uuid.Nil {
		invitedBy = nil
	}
	var m *identity.Member
	var created bool
	// The membership and its entry are one change. A new person with no password could never sign in
	// (that is what InviteUser is for), so that case refuses the whole change instead of undoing it.
	err = s.d.changeNew(ctx, c, AuditEntityUsers, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if m, created, err = identity.AddMember(ctx, s.d.db(), identity.NewMember{
			TenantID: c.tenant, Email: req.GetEmail(), Password: req.GetPassword(), DisplayName: req.GetDisplayName(), InvitedBy: invitedBy,
		}); err != nil {
			return uuid.Nil, nil, identityErr(ctx, err)
		}
		if created && req.GetPassword() == "" {
			return uuid.Nil, nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.WeakPassword,
				Message: "A password is required for a new account, or invite the person instead.", FieldErrors: map[string][]string{"password": {"required for a new account"}}})
		}
		return m.ID, map[string][2]any{"email": {nil, m.Email}, "displayName": {nil, m.DisplayName}, "roles": {nil, roles}}, nil
	})
	if err != nil {
		return nil, err
	}
	if len(roles) > 0 {
		if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
			for _, r := range roles {
				if err := rolestore.AssignRole(ctx, db, c.tenant, m.ID, r, invitedBy); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			// Undo the membership so a retry is not refused as "already a member", and say so in the
			// trail: the entry above was written when the membership was.
			_ = s.d.change(ctx, c, AuditEntityUsers, m.ID, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
				return map[string][2]any{"email": {m.Email, nil}, "undone": {nil, "the roles could not be assigned"}},
					identity.RemoveMember(ctx, s.d.db(), c.tenant, m.ID, s.d.now())
			})
			return nil, rpcError(ctx, err)
		}
	}
	u, err := s.d.user(ctx, c.tenant, m.ID)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return &CreateUserResponse{User: u, CreatedIdentity: created}, nil
}

func (s *UserAdminService) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := identity.GetMember(ctx, s.d.db(), c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	err = s.d.change(ctx, c, AuditEntityUsers, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := identity.UpdateMember(ctx, s.d.db(), c.tenant, id, req.GetDisplayName()); err != nil {
			return nil, err
		}
		after, err := identity.GetMember(ctx, s.d.db(), c.tenant, id)
		if err != nil {
			return nil, err
		}
		if before.DisplayName == after.DisplayName {
			return nil, nil
		}
		return map[string][2]any{"displayName": {before.DisplayName, after.DisplayName}}, nil
	})
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	u, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return u, nil
}

// wouldOrphanAdmins reports whether taking admin away from id (disable, remove, revoke) would
// leave the tenant with no active admin.
func (d identityDeps) wouldOrphanAdmins(ctx context.Context, tenant, id uuid.UUID) (bool, error) {
	var admins []uuid.UUID
	if err := d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		admins, err = rolestore.UsersWithRole(ctx, db, tenant, permission.RoleAdmin)
		return err
	}); err != nil {
		return false, err
	}
	if !slices.Contains(admins, id) {
		return false, nil
	}
	others := slices.DeleteFunc(admins, func(a uuid.UUID) bool { return a == id })
	_, active, err := identity.ListMembers(ctx, d.db(), tenant, identity.ListQuery{
		IDs: others, Status: identity.StatusActive, Limit: 1,
	})
	return active == 0, err
}

// guardTakingAway refuses an action on id that the caller may not take on themselves, or that
// would leave the tenant without an admin.
func (s *UserAdminService) guardTakingAway(ctx context.Context, c caller, id uuid.UUID, what string) error {
	if id == c.self {
		return svcErr(ctx, http.StatusConflict, errcode.SelfModification, "You cannot "+what+" yourself.")
	}
	orphan, err := s.d.wouldOrphanAdmins(ctx, c.tenant, id)
	if err != nil {
		return rpcError(ctx, err)
	}
	if orphan {
		return svcErr(ctx, http.StatusConflict, errcode.LastAdmin, "The tenant must keep at least one active admin.")
	}
	return s.d.guardOwner(ctx, c, id, what)
}

func (s *UserAdminService) setStatus(ctx context.Context, req *UserIdRequest, next string) (*User, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := identity.GetMember(ctx, s.d.db(), c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if next == identity.StatusDisabled {
		if err := s.guardTakingAway(ctx, c, id, "disable"); err != nil {
			return nil, err
		}
	}
	if before.Status != next {
		err := s.d.change(ctx, c, AuditEntityUsers, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
			return map[string][2]any{"status": {before.Status, next}}, identity.SetMemberStatus(ctx, s.d.db(), c.tenant, id, next, s.d.now())
		})
		if err != nil {
			return nil, identityErr(ctx, err)
		}
	}
	u, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return u, nil
}

func (s *UserAdminService) DisableUser(ctx context.Context, req *UserIdRequest) (*User, error) {
	return s.setStatus(ctx, req, identity.StatusDisabled)
}

func (s *UserAdminService) EnableUser(ctx context.Context, req *UserIdRequest) (*User, error) {
	return s.setStatus(ctx, req, identity.StatusActive)
}

func (s *UserAdminService) RemoveUser(ctx context.Context, req *UserIdRequest) (*Empty, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if err := s.guardTakingAway(ctx, c, id, "remove"); err != nil {
		return nil, err
	}
	err = s.d.change(ctx, c, AuditEntityUsers, id, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"email": {before.Email, nil}, "roles": {before.Roles, nil}},
			identity.RemoveMember(ctx, s.d.db(), c.tenant, id, s.d.now())
	})
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	// The roles live in the tenant's own database, outside the change (see identityDeps.change).
	if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
		return rolestore.RevokeAllRoles(ctx, db, c.tenant, id)
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	return &Empty{}, nil
}

func (s *UserAdminService) ResetPassword(ctx context.Context, req *ResetPasswordRequest) (*Empty, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.d.guardOwner(ctx, c, id, "reset the password of"); err != nil {
		return nil, err
	}
	m, err := identity.GetMember(ctx, s.d.db(), c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	shared, err := identity.SharedIdentities(ctx, s.d.db(), c.tenant, []uuid.UUID{m.IdentityID})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if shared[m.IdentityID] {
		return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden,
			"This person also belongs to another tenant; only they can change their password.")
	}
	if err := identity.CheckPasswordPolicy(ctx, s.d.db(), m.IdentityID, req.GetPassword(), s.d.cfg.PasswordPolicy); err != nil {
		return nil, identityErr(ctx, err)
	}
	err = s.d.change(ctx, c, AuditEntityUsers, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"password": {nil, "reset"}}, identity.SetPassword(ctx, s.d.db(), m.IdentityID, req.GetPassword(), s.d.now())
	})
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return &Empty{}, nil
}

func (s *UserAdminService) changeRole(ctx context.Context, req *RoleRequest, assign bool) (*User, error) {
	c, err := s.admin(ctx)
	if err == nil && assign {
		c, err = adminDurable(s.d, ctx)
	}
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(req.GetRole())
	before, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if req.GetScopeType() != "" || req.GetScopeId() != "" {
		return s.changeScopedRole(ctx, c, id, role, req.GetScopeType(), req.GetScopeId(), assign, before)
	}
	if assign {
		if err := s.d.checkRoles(ctx, c.tenant, []string{role}); err != nil {
			return nil, err
		}
	} else if role == permission.RoleAdmin {
		if err := s.guardTakingAway(ctx, c, id, "remove the admin role from"); err != nil {
			return nil, err
		}
	}
	// The role and its entry are one change on the tenant's own database, where user_roles live.
	err = s.d.changeInTenant(ctx, c, AuditEntityUsers, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var roles []string
		if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
			var err error
			if assign {
				by := &c.self
				if c.self == uuid.Nil {
					by = nil
				}
				err = rolestore.AssignRole(ctx, db, c.tenant, id, role, by)
			} else {
				err = rolestore.RevokeRole(ctx, db, c.tenant, id, role)
			}
			if err != nil {
				return err
			}
			held, err := rolestore.RolesForUsers(ctx, db, c.tenant, []uuid.UUID{id})
			roles = held[id]
			return err
		}); err != nil {
			return nil, err
		}
		if roles == nil {
			roles = []string{}
		}
		if slices.Equal(before.Roles, roles) {
			return nil, nil
		}
		return map[string][2]any{"roles": {before.Roles, roles}}, nil
	})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	after, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return after, nil
}

// changeScopedRole assigns or revokes a role only on one scope (roadmap 115 M1). There is no
// last-admin guard here: a role held on a scope is never the tenant's admin.
func (s *UserAdminService) changeScopedRole(ctx context.Context, c caller, id uuid.UUID, role, scopeType, scopeID string, assign bool, before *User) (*User, error) {
	if err := rolestore.ValidateScope(scopeType, scopeID); err != nil {
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: err.Error(), FieldErrors: map[string][]string{"scope": {err.Error()}}})
	}
	if assign {
		if err := s.d.checkRoles(ctx, c.tenant, []string{role}); err != nil {
			return nil, err
		}
	}
	err := s.d.changeInTenant(ctx, c, AuditEntityUsers, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var now []*ScopedRole
		if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
			var err error
			if assign {
				err = rolestore.AssignScopedRole(ctx, db, c.tenant, id, role, scopeType, scopeID, c.selfPtr())
			} else {
				err = rolestore.RevokeScopedRole(ctx, db, c.tenant, id, role, scopeType, scopeID)
			}
			if err != nil {
				return err
			}
			held, err := rolestore.ScopedRolesForUsers(ctx, db, c.tenant, []uuid.UUID{id})
			for _, sr := range held[id] {
				now = append(now, &ScopedRole{Role: sr.Role, ScopeType: sr.ScopeType, ScopeId: sr.ScopeID})
			}
			return err
		}); err != nil {
			return nil, err
		}
		if slices.EqualFunc(before.ScopedRoles, now, func(a, b *ScopedRole) bool {
			return a.GetRole() == b.GetRole() && a.GetScopeType() == b.GetScopeType() && a.GetScopeId() == b.GetScopeId()
		}) {
			return nil, nil
		}
		return map[string][2]any{"scopedRoles": {scopedRolesForAudit(before.ScopedRoles), scopedRolesForAudit(now)}}, nil
	})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	after, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return after, nil
}

func scopedRolesForAudit(in []*ScopedRole) []string {
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = r.GetRole() + "@" + r.GetScopeType() + ":" + r.GetScopeId()
	}
	return out
}

// ListScopeMembers answers "who can see this zone": every user holding a role on the scope.
func (s *UserAdminService) ListScopeMembers(ctx context.Context, req *ScopeRequest) (*ListScopeMembersResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	if err := rolestore.ValidateScope(req.GetScopeType(), req.GetScopeId()); err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	var members []rolestore.ScopeMember
	if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
		var err error
		members, err = rolestore.MembersOfScope(ctx, db, c.tenant, req.GetScopeType(), req.GetScopeId())
		return err
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	resp := &ListScopeMembersResponse{Members: []*ScopeMember{}}
	for _, m := range members {
		mem, err := identity.GetMember(ctx, s.d.db(), c.tenant, m.UserID)
		if err != nil {
			continue // removed since: the assignment is stale and shows no one
		}
		resp.Members = append(resp.Members, &ScopeMember{UserId: m.UserID.String(), Email: mem.Email,
			DisplayName: mem.DisplayName, Role: m.Role})
	}
	return resp, nil
}

func (s *UserAdminService) AssignRole(ctx context.Context, req *RoleRequest) (*User, error) {
	return s.changeRole(ctx, req, true)
}

func (s *UserAdminService) RevokeRole(ctx context.Context, req *RoleRequest) (*User, error) {
	return s.changeRole(ctx, req, false)
}

func (s *UserAdminService) ListUserAudit(ctx context.Context, req *ListUserAuditRequest) (*ListUserAuditResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	return s.d.listAudit(ctx, c, AuditEntityUsers, id, req.GetCursor(), req.GetLimit())
}

// listAudit pages entity record id's audit trail, newest first.
func (d identityDeps) listAudit(ctx context.Context, c caller, entity string, id uuid.UUID, cursor string, limit32 int32) (*ListUserAuditResponse, error) {
	if d.cfg.Audit == nil {
		return &ListUserAuditResponse{}, nil
	}
	limit := int(limit32)
	if limit <= 0 || limit > audit.MaxPageSize {
		limit = 50
	}
	q := audit.Query{Entity: entity, RecordID: id, Limit: limit}
	if cursor != "" {
		cur, ok := audit.DecodeCursor(cursor)
		if !ok {
			return nil, svcErr(ctx, http.StatusBadRequest, errcode.InvalidCursor, "Invalid cursor.")
		}
		q.After = &cur
	}
	rows, err := d.cfg.Audit.Query(ctx, c.tenant, q)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	resp := &ListUserAuditResponse{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		resp.NextCursor = audit.EncodeCursor(audit.Cursor{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	for _, r := range rows {
		ev := &AuditEvent{Id: r.ID.String(), Action: r.Action, OccurredAt: formatTime(&r.OccurredAt), Diff: jsonOr(r.Diff, "{}")}
		if r.ActorUserID != nil {
			ev.ActorUserId = r.ActorUserID.String()
		}
		resp.Events = append(resp.Events, ev)
	}
	return resp, nil
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// --- AccountService ---

func (s *AccountService) me(ctx context.Context, c caller) (*Me, error) {
	me := &Me{UserId: c.rc.UserID, TenantId: c.rc.TenantID, Roles: c.rc.Roles}
	if me.Roles == nil {
		me.Roles = []string{}
	}
	m, err := identity.GetMember(ctx, s.d.db(), c.tenant, c.self)
	if err == nil {
		me.Email, me.DisplayName = m.Email, m.DisplayName
	} else if !errors.Is(err, identity.ErrNotFound) {
		return nil, rpcError(ctx, err)
	}
	return me, nil
}

func (s *AccountService) GetMe(ctx context.Context, _ *Empty) (*Me, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	return s.me(ctx, c)
}

func (s *AccountService) UpdateProfile(ctx context.Context, req *UpdateProfileRequest) (*Me, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := identity.UpdateMember(ctx, s.d.db(), c.tenant, c.self, req.GetDisplayName()); err != nil {
		return nil, identityErr(ctx, err)
	}
	return s.me(ctx, c)
}

func (s *AccountService) ListMyTenants(ctx context.Context, _ *Empty) (*ListMyTenantsResponse, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	identityID, err := identity.IdentityOf(ctx, s.d.db(), c.tenant, c.self)
	if errors.Is(err, identity.ErrNotFound) {
		return &ListMyTenantsResponse{Tenants: []*MyTenant{}}, nil
	}
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	ms, err := identity.Memberships(ctx, s.d.db(), identityID)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := make([]*MyTenant, len(ms))
	for i, m := range ms {
		out[i] = &MyTenant{TenantId: m.TenantID.String(), Name: m.TenantName, Current: m.TenantID == c.tenant}
	}
	return &ListMyTenantsResponse{Tenants: out}, nil
}

func (s *AccountService) ListTenantUsers(ctx context.Context, req *ListTenantUsersRequest) (*ListTenantUsersResponse, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	out := &ListTenantUsersResponse{Users: []*TenantUser{}}
	want := int(req.GetLimit())
	for offset := 0; ; offset += identity.MaxListLimit {
		page, total, err := identity.ListMembers(ctx, s.d.db(), c.tenant, identity.ListQuery{
			Search: req.GetSearch(), Status: identity.StatusActive, Offset: offset, Limit: identity.MaxListLimit,
		})
		if err != nil {
			return nil, rpcError(ctx, err)
		}
		for _, m := range page {
			if want > 0 && len(out.Users) == want {
				return out, nil
			}
			out.Users = append(out.Users, &TenantUser{Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName})
		}
		if len(page) == 0 || offset+len(page) >= total {
			return out, nil
		}
	}
}
