// Package users is a tenant's people (roadmap 113-115) as an administrative slice: the members, their
// roles (tenant-wide and on one scope), invitations, passwords, two-step resets and service accounts.
// types.go, rules.go (the wire form, the guards' arithmetic), store.go (the port: the shared identity
// database and the tenant's own role tables) with its Postgres adapter, and this file, which only
// orchestrates. Anything that creates access outliving the moment (a person, a service account, a
// role, a password or MFA reset) is refused to a vendor's support session (kernel.Deps.AdminDurable).
package users

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

type Service struct {
	store  Store
	owners Owners
	notify Notifier
	deps   kernel.Deps
}

// New is the service over store. owners says who the tenant's owner is; notify emails invitations (nil
// sends nothing, and the answer carries the link instead); deps carry the clock, the audit sink and the
// caller checks.
func New(store Store, owners Owners, notify Notifier, deps kernel.Deps) *Service {
	return &Service{store: store, owners: owners, notify: notify, deps: deps}
}

// users converts members, attaching their roles and the shared-identity flag.
func (s *Service) users(ctx context.Context, tenant uuid.UUID, ms []identity.Member) ([]*User, error) {
	ids := make([]uuid.UUID, len(ms))
	people := make([]uuid.UUID, len(ms))
	for i, m := range ms {
		ids[i], people[i] = m.ID, m.IdentityID
	}
	roles, scoped, err := s.store.RolesOf(ctx, tenant, ids)
	if err != nil {
		return nil, err
	}
	shared, err := s.store.Shared(ctx, tenant, people)
	if err != nil {
		return nil, err
	}
	owner, err := s.owners.Ensure(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]*User, len(ms))
	for i, m := range ms {
		var inv *identity.Invitation
		if m.Status == identity.StatusInvited {
			if p, err := s.store.PendingInvitation(ctx, m.ID, s.deps.Now()); err == nil {
				inv = p
			}
		}
		out[i] = view(m, roles[m.ID], scoped[m.ID], shared[m.IdentityID], m.ID == owner, inv)
	}
	return out, nil
}

func (s *Service) user(ctx context.Context, tenant, id uuid.UUID) (*User, error) {
	m, err := s.store.Member(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	users, err := s.users(ctx, tenant, []identity.Member{*m})
	if err != nil {
		return nil, err
	}
	return users[0], nil
}

// userOr is user with identity's errors mapped onto the envelope.
func (s *Service) userOr(ctx context.Context, tenant, id uuid.UUID) (*User, error) {
	u, err := s.user(ctx, tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return u, nil
}

func (s *Service) List(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	q := identity.ListQuery{Search: req.GetSearch(), Status: req.GetStatus(), Offset: int(req.GetOffset()), Limit: int(req.GetLimit())}
	if role := strings.TrimSpace(req.GetRole()); role != "" {
		if q.IDs, err = s.store.WithRole(ctx, c.Tenant, role); err != nil {
			return nil, err
		}
	}
	ms, total, err := s.store.Members(ctx, c.Tenant, q)
	if err != nil {
		return nil, err
	}
	users, err := s.users(ctx, c.Tenant, ms)
	if err != nil {
		return nil, err
	}
	return &ListUsersResponse{Users: users, Total: int32(total)}, nil
}

func (s *Service) Get(ctx context.Context, req *UserIdRequest) (*User, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	return s.userOr(ctx, c.Tenant, id)
}

// checkRoles is nil when every role is in the tenant's catalog.
func (s *Service) checkRoles(ctx context.Context, tenant uuid.UUID, roles []string) error {
	known, err := s.store.KnownRoles(ctx, tenant)
	if err != nil {
		return err
	}
	return kernel.UnknownRoles(known, roles)
}

// Create adds a member with a password (or, for a person who already has an account, just a
// membership). A new person with no password could never sign in, which is what Invite is for.
func (s *Service) Create(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	roles := kernel.Dedupe(req.GetRoles())
	if err := s.checkRoles(ctx, c.Tenant, roles); err != nil {
		return nil, err
	}
	if req.GetPassword() != "" {
		// The password only applies to a new account, which belongs to this tenant alone.
		if err := s.store.CheckPassword(ctx, uuid.Nil, req.GetPassword(), c.Tenant); err != nil {
			return nil, kernel.IdentityErr(err)
		}
	}
	invitedBy := c.SelfPtr()
	var m *identity.Member
	var created bool
	// The membership and its entry are one change. A new person with no password could never sign in, so
	// that case refuses the whole change instead of undoing it.
	err = s.deps.ChangeNew(ctx, c, AuditEntity, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if m, created, err = s.store.Add(ctx, identity.NewMember{
			TenantID: c.Tenant, Email: req.GetEmail(), Password: req.GetPassword(), DisplayName: req.GetDisplayName(), InvitedBy: invitedBy,
		}); err != nil {
			return uuid.Nil, nil, kernel.IdentityErr(err)
		}
		if created && req.GetPassword() == "" {
			return uuid.Nil, nil, passwordRequired()
		}
		return m.ID, map[string][2]any{"email": {nil, m.Email}, "displayName": {nil, m.DisplayName}, "roles": {nil, roles}}, nil
	})
	if err != nil {
		return nil, err
	}
	if len(roles) > 0 {
		if err := s.store.Grant(ctx, c.Tenant, m.ID, roles, invitedBy); err != nil {
			// Undo the membership so a retry is not refused as "already a member", and say so in the trail:
			// the entry above was written when the membership was.
			_ = s.deps.Change(ctx, c, AuditEntity, m.ID, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
				return map[string][2]any{"email": {m.Email, nil}, "undone": {nil, "the roles could not be assigned"}},
					s.store.Remove(ctx, c.Tenant, m.ID, s.deps.Now())
			})
			return nil, err
		}
	}
	u, err := s.userOr(ctx, c.Tenant, m.ID)
	if err != nil {
		return nil, err
	}
	return &CreateUserResponse{User: u, CreatedIdentity: created}, nil
}

func (s *Service) Update(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.store.Member(ctx, c.Tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.Rename(ctx, c.Tenant, id, req.GetDisplayName()); err != nil {
			return nil, err
		}
		after, err := s.store.Member(ctx, c.Tenant, id)
		if err != nil {
			return nil, err
		}
		if before.DisplayName == after.DisplayName {
			return nil, nil
		}
		return map[string][2]any{"displayName": {before.DisplayName, after.DisplayName}}, nil
	})
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return s.userOr(ctx, c.Tenant, id)
}

// wouldOrphanAdmins reports whether taking admin away from id (disable, remove, revoke) would leave
// the tenant with no active admin.
func (s *Service) wouldOrphanAdmins(ctx context.Context, tenant, id uuid.UUID) (bool, error) {
	admins, err := s.store.WithRole(ctx, tenant, permission.RoleAdmin)
	if err != nil {
		return false, err
	}
	others, wasAdmin := otherAdmins(admins, id)
	if !wasAdmin {
		return false, nil
	}
	_, active, err := s.store.Members(ctx, tenant, identity.ListQuery{IDs: others, Status: identity.StatusActive, Limit: 1})
	return active == 0, err
}

// guardTakingAway refuses an action on id that the caller may not take on themselves, or that would
// leave the tenant without an admin.
func (s *Service) guardTakingAway(ctx context.Context, c kernel.Caller, id uuid.UUID, what string) error {
	if id == c.Self {
		return selfModification(what)
	}
	orphan, err := s.wouldOrphanAdmins(ctx, c.Tenant, id)
	if err != nil {
		return err
	}
	if orphan {
		return lastAdmin()
	}
	return s.owners.Guard(ctx, c, id, what)
}

func (s *Service) setStatus(ctx context.Context, req *UserIdRequest, next string) (*User, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.store.Member(ctx, c.Tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	if next == identity.StatusDisabled {
		if err := s.guardTakingAway(ctx, c, id, "disable"); err != nil {
			return nil, err
		}
	}
	if before.Status != next {
		err := s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
			return map[string][2]any{"status": {before.Status, next}}, s.store.SetStatus(ctx, c.Tenant, id, next, s.deps.Now())
		})
		if err != nil {
			return nil, kernel.IdentityErr(err)
		}
	}
	return s.userOr(ctx, c.Tenant, id)
}

func (s *Service) Disable(ctx context.Context, req *UserIdRequest) (*User, error) {
	return s.setStatus(ctx, req, identity.StatusDisabled)
}

func (s *Service) Enable(ctx context.Context, req *UserIdRequest) (*User, error) {
	return s.setStatus(ctx, req, identity.StatusActive)
}

func (s *Service) Remove(ctx context.Context, req *UserIdRequest) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.userOr(ctx, c.Tenant, id)
	if err != nil {
		return nil, err
	}
	if err := s.guardTakingAway(ctx, c, id, "remove"); err != nil {
		return nil, err
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"email": {before.Email, nil}, "roles": {before.Roles, nil}},
			s.store.Remove(ctx, c.Tenant, id, s.deps.Now())
	})
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	// The roles live in the tenant's own database, outside the change.
	if err := s.store.RevokeAll(ctx, c.Tenant, id); err != nil {
		return nil, err
	}
	return &kernel.Empty{}, nil
}

func (s *Service) ResetPassword(ctx context.Context, req *ResetPasswordRequest) (*kernel.Empty, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.owners.Guard(ctx, c, id, "reset the password of"); err != nil {
		return nil, err
	}
	m, err := s.store.Member(ctx, c.Tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	shared, err := s.store.Shared(ctx, c.Tenant, []uuid.UUID{m.IdentityID})
	if err != nil {
		return nil, err
	}
	if shared[m.IdentityID] {
		return nil, sharedPerson("password")
	}
	if err := s.store.CheckPassword(ctx, m.IdentityID, req.GetPassword(), uuid.Nil); err != nil {
		return nil, kernel.IdentityErr(err)
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"password": {nil, "reset"}}, s.store.SetPassword(ctx, m.IdentityID, req.GetPassword(), s.deps.Now())
	})
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return &kernel.Empty{}, nil
}

// ResetMfa turns off someone else's two-step sign-in. A person's own settings are internal/admin/mfa;
// this needs the owner and shared-identity guards of user administration.
func (s *Service) ResetMfa(ctx context.Context, req *UserIdRequest) (*kernel.Empty, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.owners.Guard(ctx, c, id, "reset the MFA of"); err != nil {
		return nil, err
	}
	m, err := s.store.Member(ctx, c.Tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	shared, err := s.store.Shared(ctx, c.Tenant, []uuid.UUID{m.IdentityID})
	if err != nil {
		return nil, err
	}
	if shared[m.IdentityID] {
		return nil, sharedPerson("two-factor settings")
	}
	if err := s.store.DisableMFA(ctx, m.IdentityID); err != nil {
		return nil, kernel.IdentityErr(err)
	}
	s.deps.RecordFor(ctx, c, AuditEntity, id, audit.ActionUpdate, map[string][2]any{"mfa": {"on", "reset"}})
	return &kernel.Empty{}, nil
}

func (s *Service) changeRole(ctx context.Context, req *RoleRequest, assign bool) (*User, error) {
	c, err := s.deps.Admin(ctx)
	if err == nil && assign {
		c, err = s.deps.AdminDurable(ctx)
	}
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(req.GetRole())
	before, err := s.userOr(ctx, c.Tenant, id)
	if err != nil {
		return nil, err
	}
	if req.GetScopeType() != "" || req.GetScopeId() != "" {
		return s.changeScopedRole(ctx, c, id, role, req.GetScopeType(), req.GetScopeId(), assign, before)
	}
	if assign {
		if err := s.checkRoles(ctx, c.Tenant, []string{role}); err != nil {
			return nil, err
		}
	} else if role == permission.RoleAdmin {
		if err := s.guardTakingAway(ctx, c, id, "remove the admin role from"); err != nil {
			return nil, err
		}
	}
	// The role and its entry are one change on the tenant's own database, where user_roles live.
	err = s.deps.ChangeInTenant(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		roles, err := s.store.ChangeRole(ctx, c.Tenant, id, role, assign, c.SelfPtr())
		if err != nil {
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
		return nil, err
	}
	return s.userOr(ctx, c.Tenant, id)
}

// changeScopedRole assigns or revokes a role only on one scope (roadmap 115 M1). There is no last-admin
// guard here: a role held on a scope is never the tenant's admin.
func (s *Service) changeScopedRole(ctx context.Context, c kernel.Caller, id uuid.UUID, role, scopeType, scopeID string, assign bool, before *User) (*User, error) {
	if err := rolestore.ValidateScope(scopeType, scopeID); err != nil {
		return nil, scopeInvalid(err)
	}
	if assign {
		if err := s.checkRoles(ctx, c.Tenant, []string{role}); err != nil {
			return nil, err
		}
	}
	err := s.deps.ChangeInTenant(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		held, err := s.store.ChangeScopedRole(ctx, c.Tenant, id, role, scopeType, scopeID, assign, c.SelfPtr())
		if err != nil {
			return nil, err
		}
		now := scopedView(held)
		if sameScoped(before.ScopedRoles, now) {
			return nil, nil
		}
		return map[string][2]any{"scopedRoles": {scopedForAudit(before.ScopedRoles), scopedForAudit(now)}}, nil
	})
	if err != nil {
		return nil, err
	}
	return s.userOr(ctx, c.Tenant, id)
}

func (s *Service) AssignRole(ctx context.Context, req *RoleRequest) (*User, error) {
	return s.changeRole(ctx, req, true)
}

func (s *Service) RevokeRole(ctx context.Context, req *RoleRequest) (*User, error) {
	return s.changeRole(ctx, req, false)
}

// ListScopeMembers answers "who can see this zone": every user holding a role on the scope.
func (s *Service) ListScopeMembers(ctx context.Context, req *ScopeRequest) (*ListScopeMembersResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	if err := rolestore.ValidateScope(req.GetScopeType(), req.GetScopeId()); err != nil {
		return nil, fail(http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	members, err := s.store.ScopeMembers(ctx, c.Tenant, req.GetScopeType(), req.GetScopeId())
	if err != nil {
		return nil, err
	}
	resp := &ListScopeMembersResponse{Members: []*ScopeMember{}}
	for _, m := range members {
		mem, err := s.store.Member(ctx, c.Tenant, m.UserID)
		if err != nil {
			continue // removed since: the assignment is stale and shows no one
		}
		resp.Members = append(resp.Members, &ScopeMember{UserId: m.UserID.String(), Email: mem.Email,
			DisplayName: mem.DisplayName, Role: m.Role})
	}
	return resp, nil
}

func (s *Service) ListAudit(ctx context.Context, req *ListUserAuditRequest) (*kernel.ListUserAuditResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	return s.deps.ListAudit(ctx, c, AuditEntity, id, req.GetCursor(), req.GetLimit())
}

// Invite adds someone as an invited member and emails them a link; accepting it (REST
// /auth/invitations/accept, which sets the session cookie) activates them.
func (s *Service) Invite(ctx context.Context, req *InviteUserRequest) (*InviteUserResponse, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	roles := kernel.Dedupe(req.GetRoles())
	if err := s.checkRoles(ctx, c.Tenant, roles); err != nil {
		return nil, err
	}
	invitedBy := c.SelfPtr()
	m, _, err := s.store.Add(ctx, identity.NewMember{
		TenantID: c.Tenant, Email: req.GetEmail(), DisplayName: req.GetDisplayName(), InvitedBy: invitedBy,
		Status: identity.StatusInvited,
	})
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	if len(roles) > 0 {
		if err := s.store.Grant(ctx, c.Tenant, m.ID, roles, invitedBy); err != nil {
			_ = s.store.Remove(ctx, c.Tenant, m.ID, s.deps.Now())
			return nil, err
		}
	}
	resp, err := s.issueInvitation(ctx, c, m.ID)
	if err != nil {
		_ = s.store.Remove(ctx, c.Tenant, m.ID, s.deps.Now())
		return nil, err
	}
	s.deps.RecordFor(ctx, c, AuditEntity, m.ID, audit.ActionCreate, map[string][2]any{
		"email": {nil, m.Email}, "displayName": {nil, m.DisplayName}, "roles": {nil, roles},
		"status": {nil, identity.StatusInvited},
	})
	return resp, nil
}

func (s *Service) ResendInvitation(ctx context.Context, req *UserIdRequest) (*InviteUserResponse, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	resp, err := s.issueInvitation(ctx, c, id)
	if err != nil {
		return nil, err
	}
	s.deps.RecordFor(ctx, c, AuditEntity, id, audit.ActionUpdate, map[string][2]any{"invitation": {nil, "resent"}})
	return resp, nil
}

// issueInvitation creates a fresh link for invited member id and emails it. A relay failure is not an
// error: the invitation stands and the link comes back for the admin to pass on.
func (s *Service) issueInvitation(ctx context.Context, c kernel.Caller, id uuid.UUID) (*InviteUserResponse, error) {
	m, err := s.store.Member(ctx, c.Tenant, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	if m.Status != identity.StatusInvited {
		return nil, fail(http.StatusConflict, errcode.AlreadyMember, "This person has already joined the tenant.")
	}
	token, inv, newAccount, err := s.store.NewInvitation(ctx, c.Tenant, m, c.SelfPtr(), s.deps.Now())
	if err != nil {
		return nil, err
	}
	// Without a mailer the link is relative: the portal resolves it.
	link := (*mail.Mailer)(nil).Link("/accept-invite", token)
	var sendErr error = mail.ErrNotConfigured
	if s.notify != nil {
		link = s.notify.Link("/accept-invite", token)
		sendErr = s.notify.SendInvitation(ctx, mail.Invitation{
			To: m.Email, TenantName: inv.TenantName, InviterName: s.deps.DisplayNameOf(ctx, c),
			Link: link, ExpiresAt: inv.ExpiresAt, NewAccount: newAccount,
		})
	}
	if sendErr != nil && !errors.Is(sendErr, mail.ErrNotConfigured) {
		slog.Warn("invitation email not sent", "member_id", m.ID, standard.LogError, sendErr)
	}
	u, err := s.userOr(ctx, c.Tenant, m.ID)
	if err != nil {
		return nil, err
	}
	resp := &InviteUserResponse{User: u, ExpiresAt: kernel.FormatTime(&inv.ExpiresAt), EmailSent: sendErr == nil}
	if sendErr != nil {
		resp.AcceptUrl = link
	}
	return resp, nil
}

func (s *Service) CreateServiceAccount(ctx context.Context, req *CreateServiceAccountRequest) (*User, error) {
	c, err := s.deps.AdminDurable(ctx)
	if err != nil {
		return nil, err
	}
	roles := kernel.Dedupe(req.GetRoles())
	if err := s.checkRoles(ctx, c.Tenant, roles); err != nil {
		return nil, err
	}
	createdBy := c.SelfPtr()
	m, err := s.store.CreateServiceAccount(ctx, c.Tenant, req.GetName(), createdBy)
	if err != nil {
		return nil, fail(http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	if len(roles) > 0 {
		if err := s.store.Grant(ctx, c.Tenant, m.ID, roles, createdBy); err != nil {
			_ = s.store.Remove(ctx, c.Tenant, m.ID, s.deps.Now())
			return nil, err
		}
	}
	s.deps.RecordFor(ctx, c, AuditEntity, m.ID, audit.ActionCreate, map[string][2]any{
		"serviceAccount": {nil, m.DisplayName}, "roles": {nil, roles},
	})
	return s.userOr(ctx, c.Tenant, m.ID)
}

func (s *Service) ListServiceAccounts(ctx context.Context, _ *kernel.Empty) (*ListUsersResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ServiceAccounts(ctx, c.Tenant)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	users, err := s.users(ctx, c.Tenant, ms)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return &ListUsersResponse{Users: users, Total: int32(len(users))}, nil
}
