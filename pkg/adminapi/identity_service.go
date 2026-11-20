// UserAdminService and AccountService (roadmap 113): user administration for a tenant's admins
// and the caller's own account, over pkg/identity. Members live in the shared database
// (IdentityConfig.Shared); roles in each tenant's own, through Router. Served only by the
// token-issuing service, which owns identities.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntityUsers is the audit-trail entity name user administration records under.
const AuditEntityUsers = "metadata.users"

// RolePlatformAdmin is the built-in role of the platform tenant.
const RolePlatformAdmin = "platform_admin"

// IdentityConfig turns on UserAdminService and AccountService on a gRPC listener.
type IdentityConfig struct {
	Shared *pgxpool.Pool
	Router *control.Router
	// Audit records every administrative change; nil records nothing.
	Audit audit.Store
	// PublicURL is the origin people reach the service at; the SSO redirect URI is built from it.
	PublicURL string
	// Mailer sends invitation emails; nil sends none (the admin gets the link back instead).
	Mailer *accountmail.Mailer
	// PasswordPolicy is each tenant's minimum password length; nil means the platform minimum.
	PasswordPolicy identity.PasswordPolicy
	// APIKeyMaxDays is the tenant's cap on an API key's lifetime in days (auth.apiKeyMaxLifetimeDays);
	// nil or 0 means no tenant cap.
	APIKeyMaxDays func(ctx context.Context, tenant uuid.UUID) int
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
	c, err := d.caller(ctx)
	if err != nil {
		return caller{}, err
	}
	if !c.rc.IsAdmin() {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the admin role")
	}
	if !c.rc.ScopePermitsAdminSurface() {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the `admin` OAuth2 scope")
	}
	return c, nil
}

func svcErr(ctx context.Context, httpStatus uint16, code, message string) error {
	return sendServiceError(ctx, crud.NewServiceErrorMsg(httpStatus, code, message))
}

// identityErr maps pkg/identity's errors onto the platform error envelope.
func identityErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return svcErr(ctx, http.StatusNotFound, errcode.NotFound, "User not found.")
	case errors.Is(err, identity.ErrAlreadyMember):
		return svcErr(ctx, http.StatusConflict, errcode.AlreadyMember, "This person is already a member of the tenant.")
	case errors.Is(err, identity.ErrWeakPassword):
		return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.WeakPassword,
			Message: err.Error(), FieldErrors: map[string][]string{"password": {err.Error()}}})
	case errors.Is(err, identity.ErrMFAInvalidCode):
		return svcErr(ctx, http.StatusUnauthorized, errcode.MFAInvalidCode, err.Error())
	case errors.Is(err, identity.ErrMFAAlreadyOn):
		return svcErr(ctx, http.StatusConflict, errcode.MFAAlreadyOn, err.Error())
	case errors.Is(err, identity.ErrMFANotEnrolled), errors.Is(err, identity.ErrMFAEnrollNotRun):
		return svcErr(ctx, http.StatusConflict, errcode.MFANotEnrolled, err.Error())
	case errors.Is(err, identity.ErrInvalidEmail):
		return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: err.Error(), FieldErrors: map[string][]string{"email": {err.Error()}}})
	}
	return rpcError(ctx, err)
}

// withTenantTx runs fn in a transaction on tenant's own database (where user_roles live).
func (d identityDeps) withTenantTx(ctx context.Context, tenant uuid.UUID, fn func(db peripherals.DBTX) error) error {
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
	if err := d.withTenantTx(ctx, tenant, func(db peripherals.DBTX) error {
		var err error
		if roles, err = peripherals.RolesForUsers(ctx, db, tenant, ids); err != nil {
			return err
		}
		scoped, err = peripherals.ScopedRolesForUsers(ctx, db, tenant, ids)
		return err
	}); err != nil {
		return nil, err
	}
	shared, err := identity.SharedIdentities(ctx, d.cfg.Shared, tenant, identities)
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
			if inv, err := identity.PendingInvitation(ctx, d.cfg.Shared, m.ID, d.now()); err == nil && inv != nil {
				u.InvitationExpiresAt = formatTime(&inv.ExpiresAt)
			}
		}
		out[i] = u
	}
	return out, nil
}

func (d identityDeps) user(ctx context.Context, tenant, id uuid.UUID) (*User, error) {
	m, err := identity.GetMember(ctx, d.cfg.Shared, tenant, id)
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

// recordFor is record for any administered entity (metadata.users, metadata.roles).
func (d identityDeps) recordFor(ctx context.Context, c caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	if d.cfg.Audit == nil {
		return
	}
	obj := audit.JsonObject{}
	for field, ba := range diff {
		raw, err := json.Marshal(map[string]any{"before": ba[0], "after": ba[1]})
		if err == nil {
			obj[field] = raw
		}
	}
	entry := audit.Entry{TenantID: c.tenant, Entity: entity, RecordID: id, Action: action,
		Diff: obj, OccurredAt: d.now()}
	if c.self != uuid.Nil {
		self := c.self
		entry.ActorUserID = &self
	}
	_ = d.cfg.Audit.Record(ctx, c.tenant, entry)
}

func parseID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, crud.NewServiceErrorMsg(http.StatusBadRequest, errcode.ValidationFailed, "`id` must be a UUID")
	}
	return parsed, nil
}

func (s *UserAdminService) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	q := identity.ListQuery{Search: req.GetSearch(), Status: req.GetStatus(), Offset: int(req.GetOffset()), Limit: int(req.GetLimit())}
	if role := strings.TrimSpace(req.GetRole()); role != "" {
		if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
			q.IDs, err = peripherals.UsersWithRole(ctx, db, c.tenant, role)
			return err
		}); err != nil {
			return nil, rpcError(ctx, err)
		}
	}
	ms, total, err := identity.ListMembers(ctx, s.d.cfg.Shared, c.tenant, q)
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
	err := d.withTenantTx(ctx, tenant, func(db peripherals.DBTX) error {
		if err := peripherals.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		var err error
		roles, err = peripherals.RoleNames(ctx, db, tenant)
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
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	roles := dedupe(req.GetRoles())
	if err := s.d.checkRoles(ctx, c.tenant, roles); err != nil {
		return nil, err
	}
	if req.GetPassword() != "" {
		// The password only applies to a new account, which belongs to this tenant alone.
		if err := identity.CheckPasswordPolicy(ctx, s.d.cfg.Shared, uuid.Nil, req.GetPassword(), s.d.cfg.PasswordPolicy, c.tenant); err != nil {
			return nil, identityErr(ctx, err)
		}
	}
	invitedBy := &c.self
	if c.self == uuid.Nil {
		invitedBy = nil
	}
	m, created, err := identity.AddMember(ctx, s.d.cfg.Shared, identity.NewMember{
		TenantID: c.tenant, Email: req.GetEmail(), Password: req.GetPassword(), DisplayName: req.GetDisplayName(), InvitedBy: invitedBy,
	})
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if created && req.GetPassword() == "" {
		// A new person with no password could never sign in: that is what InviteUser is for.
		_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, m.ID, s.d.now())
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.WeakPassword,
			Message: "A password is required for a new account, or invite the person instead.", FieldErrors: map[string][]string{"password": {"required for a new account"}}})
	}
	if len(roles) > 0 {
		if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
			for _, r := range roles {
				if err := peripherals.AssignRole(ctx, db, c.tenant, m.ID, r, invitedBy); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			// Undo the membership so a retry is not refused as "already a member".
			_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, m.ID, s.d.now())
			return nil, rpcError(ctx, err)
		}
	}
	s.d.record(ctx, c, m.ID, audit.ActionCreate, map[string][2]any{
		"email": {nil, m.Email}, "displayName": {nil, m.DisplayName}, "roles": {nil, roles},
	})
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
	before, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if err := identity.UpdateMember(ctx, s.d.cfg.Shared, c.tenant, id, req.GetDisplayName()); err != nil {
		return nil, identityErr(ctx, err)
	}
	u, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if before.DisplayName != u.DisplayName {
		s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"displayName": {before.DisplayName, u.DisplayName}})
	}
	return u, nil
}

// wouldOrphanAdmins reports whether taking admin away from id (disable, remove, revoke) would
// leave the tenant with no active admin.
func (d identityDeps) wouldOrphanAdmins(ctx context.Context, tenant, id uuid.UUID) (bool, error) {
	var admins []uuid.UUID
	if err := d.withTenantTx(ctx, tenant, func(db peripherals.DBTX) error {
		var err error
		admins, err = peripherals.UsersWithRole(ctx, db, tenant, permission.RoleAdmin)
		return err
	}); err != nil {
		return false, err
	}
	if !slices.Contains(admins, id) {
		return false, nil
	}
	others := slices.DeleteFunc(admins, func(a uuid.UUID) bool { return a == id })
	_, active, err := identity.ListMembers(ctx, d.cfg.Shared, tenant, identity.ListQuery{
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
	before, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if next == identity.StatusDisabled {
		if err := s.guardTakingAway(ctx, c, id, "disable"); err != nil {
			return nil, err
		}
	}
	if before.Status != next {
		if err := identity.SetMemberStatus(ctx, s.d.cfg.Shared, c.tenant, id, next, s.d.now()); err != nil {
			return nil, identityErr(ctx, err)
		}
		s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"status": {before.Status, next}})
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
	if err := identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, id, s.d.now()); err != nil {
		return nil, identityErr(ctx, err)
	}
	if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
		return peripherals.RevokeAllRoles(ctx, db, c.tenant, id)
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	s.d.record(ctx, c, id, audit.ActionDelete, map[string][2]any{
		"email": {before.Email, nil}, "roles": {before.Roles, nil},
	})
	return &Empty{}, nil
}

func (s *UserAdminService) ResetPassword(ctx context.Context, req *ResetPasswordRequest) (*Empty, error) {
	c, err := s.admin(ctx)
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
	m, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	shared, err := identity.SharedIdentities(ctx, s.d.cfg.Shared, c.tenant, []uuid.UUID{m.IdentityID})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if shared[m.IdentityID] {
		return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden,
			"This person also belongs to another tenant; only they can change their password.")
	}
	if err := identity.CheckPasswordPolicy(ctx, s.d.cfg.Shared, m.IdentityID, req.GetPassword(), s.d.cfg.PasswordPolicy); err != nil {
		return nil, identityErr(ctx, err)
	}
	if err := identity.SetPassword(ctx, s.d.cfg.Shared, m.IdentityID, req.GetPassword(), s.d.now()); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"password": {nil, "reset"}})
	return &Empty{}, nil
}

func (s *UserAdminService) changeRole(ctx context.Context, req *RoleRequest, assign bool) (*User, error) {
	c, err := s.admin(ctx)
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
	if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
		if assign {
			by := &c.self
			if c.self == uuid.Nil {
				by = nil
			}
			return peripherals.AssignRole(ctx, db, c.tenant, id, role, by)
		}
		return peripherals.RevokeRole(ctx, db, c.tenant, id, role)
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	after, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if !slices.Equal(before.Roles, after.Roles) {
		s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"roles": {before.Roles, after.Roles}})
	}
	return after, nil
}

// changeScopedRole assigns or revokes a role only on one scope (roadmap 115 M1). There is no
// last-admin guard here: a role held on a scope is never the tenant's admin.
func (s *UserAdminService) changeScopedRole(ctx context.Context, c caller, id uuid.UUID, role, scopeType, scopeID string, assign bool, before *User) (*User, error) {
	if err := peripherals.ValidateScope(scopeType, scopeID); err != nil {
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: err.Error(), FieldErrors: map[string][]string{"scope": {err.Error()}}})
	}
	if assign {
		if err := s.d.checkRoles(ctx, c.tenant, []string{role}); err != nil {
			return nil, err
		}
	}
	if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
		if assign {
			return peripherals.AssignScopedRole(ctx, db, c.tenant, id, role, scopeType, scopeID, c.selfPtr())
		}
		return peripherals.RevokeScopedRole(ctx, db, c.tenant, id, role, scopeType, scopeID)
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	after, err := s.d.user(ctx, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if !slices.EqualFunc(before.ScopedRoles, after.ScopedRoles, func(a, b *ScopedRole) bool {
		return a.GetRole() == b.GetRole() && a.GetScopeType() == b.GetScopeType() && a.GetScopeId() == b.GetScopeId()
	}) {
		s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"scopedRoles": {scopedRolesForAudit(before.ScopedRoles), scopedRolesForAudit(after.ScopedRoles)}})
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
	if err := peripherals.ValidateScope(req.GetScopeType(), req.GetScopeId()); err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	var members []peripherals.ScopeMember
	if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
		var err error
		members, err = peripherals.MembersOfScope(ctx, db, c.tenant, req.GetScopeType(), req.GetScopeId())
		return err
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	resp := &ListScopeMembersResponse{Members: []*ScopeMember{}}
	for _, m := range members {
		mem, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, m.UserID)
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
	m, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, c.self)
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
	if err := identity.UpdateMember(ctx, s.d.cfg.Shared, c.tenant, c.self, req.GetDisplayName()); err != nil {
		return nil, identityErr(ctx, err)
	}
	return s.me(ctx, c)
}

func (s *AccountService) ListMyTenants(ctx context.Context, _ *Empty) (*ListMyTenantsResponse, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	identityID, err := identity.IdentityOf(ctx, s.d.cfg.Shared, c.tenant, c.self)
	if errors.Is(err, identity.ErrNotFound) {
		return &ListMyTenantsResponse{Tenants: []*MyTenant{}}, nil
	}
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	ms, err := identity.Memberships(ctx, s.d.cfg.Shared, identityID)
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
		page, total, err := identity.ListMembers(ctx, s.d.cfg.Shared, c.tenant, identity.ListQuery{
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
