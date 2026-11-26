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

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// OwnerService is roadmap 115 M8: a tenant owner who is more than an admin, and a recovery path
// the tenant can see coming and cancel.
type OwnerService struct {
	d identityDeps
}

func NewOwnerService(cfg IdentityConfig) *OwnerService {
	return &OwnerService{d: identityDeps{cfg: cfg}}
}

// activeAdmins lists the tenant's active members holding the tenant-wide admin role.
func (d identityDeps) activeAdmins(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error) {
	var ids []uuid.UUID
	if err := d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		ids, err = rolestore.UsersWithRole(ctx, db, tenant, permission.RoleAdmin)
		return err
	}); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ms, _, err := identity.ListMembers(ctx, d.db(), tenant, identity.ListQuery{IDs: ids, Status: identity.StatusActive, Limit: 200})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ms, func(m identity.Member) bool { return m.AuthProvider == identity.ServiceAccountProvider }), nil
}

// ensureOwner returns the tenant's owner, assigning the oldest active admin when none is set yet
// (tenants that predate owners, and brand-new ones, are covered the first time anything asks).
func (d identityDeps) ensureOwner(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	owner, err := identity.OwnerID(ctx, d.db(), tenant)
	if err != nil || owner != uuid.Nil {
		return owner, err
	}
	admins, err := d.activeAdmins(ctx, tenant)
	if err != nil || len(admins) == 0 {
		return uuid.Nil, err
	}
	oldest := admins[0]
	for _, m := range admins[1:] {
		if m.CreatedAt.Before(oldest.CreatedAt) {
			oldest = m
		}
	}
	if err := identity.SetOwner(ctx, d.cfg.Shared, tenant, oldest.ID); err != nil {
		return uuid.Nil, err
	}
	return oldest.ID, nil
}

// guardOwner refuses an action on the owner by anyone but the owner.
func (d identityDeps) guardOwner(ctx context.Context, c caller, target uuid.UUID, what string) error {
	owner, err := d.ensureOwner(ctx, c.Tenant)
	if err != nil {
		return rpcError(ctx, err)
	}
	if owner != uuid.Nil && target == owner && c.Self != owner {
		return svcErr(ctx, http.StatusForbidden, errcode.OwnerProtected, "Only the tenant owner can "+what+" the owner.")
	}
	return nil
}

func (s *OwnerService) status(ctx context.Context, tenant uuid.UUID) (*OwnerStatus, error) {
	owner, err := s.d.ensureOwner(ctx, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := &OwnerStatus{}
	if owner != uuid.Nil {
		out.OwnerMemberId = owner.String()
		if m, err := identity.GetMember(ctx, s.d.db(), tenant, owner); err == nil {
			out.OwnerEmail = m.Email
		}
	}
	if r, err := identity.PendingOwnerRecovery(ctx, s.d.db(), tenant); err == nil {
		out.PendingRecovery = toPBRecovery(r)
	} else if !errors.Is(err, identity.ErrNoRecovery) {
		return nil, rpcError(ctx, err)
	}
	return out, nil
}

func toPBRecovery(r *identity.OwnerRecovery) *OwnerRecovery {
	return &OwnerRecovery{Id: r.ID.String(), NewOwnerMemberId: r.NewOwnerMember.String(), RequestedByEmail: r.RequestedByEmail,
		Reason: r.Reason, EffectiveAt: formatTime(&r.EffectiveAt), CreatedAt: formatTime(&r.CreatedAt), CompletedAt: formatTime(r.CompletedAt)}
}

func ownerErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, identity.ErrRecoveryPending):
		return svcErr(ctx, http.StatusConflict, errcode.ValidationFailed, err.Error())
	case errors.Is(err, identity.ErrRecoveryNotReady):
		return svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
	case errors.Is(err, identity.ErrNoRecovery), errors.Is(err, identity.ErrNotFound):
		return svcErr(ctx, http.StatusNotFound, errcode.NotFound, err.Error())
	case errors.Is(err, identity.ErrSupportReason):
		return svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "A reason of 1-500 characters is required.")
	}
	return rpcError(ctx, err)
}

func (s *OwnerService) GetOwnerStatus(ctx context.Context, _ *Empty) (*OwnerStatus, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, c.Tenant)
}

// PlatformTenantOwner is GetOwnerStatus for a platform admin on any tenant, plus the active people
// who could take over. Read-only: unlike the tenant's own view it does not name a default owner
// when none is set (that absence is exactly the case a recovery exists for).
func (s *OwnerService) PlatformTenantOwner(ctx context.Context, req *TenantIdOnlyRequest) (*TenantOwnerView, error) {
	if _, err := s.d.platformAdmin(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
	}
	owner, err := identity.OwnerID(ctx, s.d.db(), tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	view := &TenantOwnerView{Owner: &OwnerStatus{}, People: []*TenantPerson{}}
	if owner != uuid.Nil {
		view.Owner.OwnerMemberId = owner.String()
	}
	if r, err := identity.PendingOwnerRecovery(ctx, s.d.db(), tenant); err == nil {
		view.Owner.PendingRecovery = toPBRecovery(r)
	} else if !errors.Is(err, identity.ErrNoRecovery) {
		return nil, rpcError(ctx, err)
	}
	members, _, err := identity.ListMembers(ctx, s.d.cfg.Shared, tenant, identity.ListQuery{Status: identity.StatusActive, Limit: identity.MaxListLimit})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	for _, m := range members {
		if m.AuthProvider == identity.ServiceAccountProvider {
			continue
		}
		isOwner := m.ID == owner
		if isOwner {
			view.Owner.OwnerEmail = m.Email
		}
		view.People = append(view.People, &TenantPerson{Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName, IsOwner: isOwner})
	}
	return view, nil
}

func (s *OwnerService) TransferOwnership(ctx context.Context, req *TransferOwnershipRequest) (*OwnerStatus, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.d.ensureOwner(ctx, c.Tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if owner == uuid.Nil || c.Self != owner {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.OwnerProtected, "Only the tenant owner can transfer ownership.")
	}
	target, err := parseID(req.GetMemberId())
	if err != nil {
		return nil, err
	}
	if target == owner {
		return s.status(ctx, c.Tenant)
	}
	admins, err := s.d.activeAdmins(ctx, c.Tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if !slices.ContainsFunc(admins, func(m identity.Member) bool { return m.ID == target }) {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "The new owner must be an active admin of this tenant.")
	}
	err = s.d.change(ctx, c, AuditEntityOwner, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"owner": {owner.String(), target.String()}}, identity.SetOwner(ctx, s.d.db(), c.Tenant, target)
	})
	if err != nil {
		return nil, ownerErr(ctx, err)
	}
	return s.status(ctx, c.Tenant)
}

func (s *OwnerService) CancelOwnerRecovery(ctx context.Context, _ *Empty) (*OwnerStatus, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	err = s.d.change(ctx, c, AuditEntityOwner, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		r, err := identity.CancelOwnerRecovery(ctx, s.d.db(), c.Tenant, c.Self, s.d.now())
		if err != nil {
			return nil, err
		}
		return map[string][2]any{"ownerRecovery": {"pending for " + r.NewOwnerMember.String(), "cancelled"}}, nil
	})
	if err != nil {
		return nil, ownerErr(ctx, err)
	}
	return s.status(ctx, c.Tenant)
}

func (s *OwnerService) tenantName(ctx context.Context, tenant uuid.UUID) string {
	var name string
	_ = s.d.cfg.Shared.QueryRow(ctx, `SELECT name FROM control.tenants WHERE id = $1`, tenant).Scan(&name)
	return name
}

// notifyAdmins emails every active admin of the tenant (best effort: the recovery stands either way).
func (s *OwnerService) notifyAdmins(ctx context.Context, tenant uuid.UUID, n mail.OwnerRecoveryNotice) {
	if s.d.cfg.Mailer == nil {
		return
	}
	admins, err := s.d.activeAdmins(ctx, tenant)
	if err != nil {
		return
	}
	n.TenantName = s.tenantName(ctx, tenant)
	for _, a := range admins {
		n.To = a.Email
		_ = s.d.cfg.Mailer.SendOwnerRecoveryNotice(ctx, n)
	}
}

func (s *OwnerService) RequestOwnerRecovery(ctx context.Context, req *RequestOwnerRecoveryRequest) (*OwnerRecovery, error) {
	c, err := s.d.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
	}
	newOwner, err := parseID(req.GetNewOwnerMemberId())
	if err != nil {
		return nil, err
	}
	target, err := identity.GetMember(ctx, s.d.db(), tenant, newOwner)
	if err != nil || target.Status != identity.StatusActive || target.AuthProvider == identity.ServiceAccountProvider {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "The new owner must be an active person in the tenant.")
	}
	person, err := identity.GetMember(ctx, s.d.db(), tenancy.PlatformTenantID, c.Self)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	now := s.d.now()
	var r *identity.OwnerRecovery
	err = s.d.change(ctx, caller{RC: c.RC, Tenant: tenant, Self: c.Self}, AuditEntityOwner, tenant, audit.ActionUpdate,
		func(ctx context.Context) (map[string][2]any, error) {
			var err error
			if r, err = identity.RequestOwnerRecovery(ctx, s.d.db(), tenant, newOwner, c.Self, person.Email, req.GetReason(), now); err != nil {
				return nil, err
			}
			return map[string][2]any{"ownerRecovery": {nil, "requested for " + target.Email + " (effective " + r.EffectiveAt.UTC().Format(time.RFC3339) + ")"}}, nil
		})
	if err != nil {
		return nil, ownerErr(ctx, err)
	}
	s.notifyAdmins(ctx, tenant, mail.OwnerRecoveryNotice{NewOwner: target.Email, RequestedBy: person.Email,
		Reason: r.Reason, EffectiveAt: r.EffectiveAt})
	return toPBRecovery(r), nil
}

func (s *OwnerService) CompleteOwnerRecovery(ctx context.Context, req *TenantIdOnlyRequest) (*OwnerRecovery, error) {
	c, err := s.d.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
	}
	pending, err := identity.PendingOwnerRecovery(ctx, s.d.db(), tenant)
	if err != nil {
		return nil, ownerErr(ctx, err)
	}
	now := s.d.now()
	if now.Before(pending.EffectiveAt) {
		return nil, ownerErr(ctx, identity.ErrRecoveryNotReady)
	}
	target, err := identity.GetMember(ctx, s.d.db(), tenant, pending.NewOwnerMember)
	if err != nil || target.Status != identity.StatusActive {
		return nil, svcErr(ctx, http.StatusConflict, errcode.ValidationFailed, "The proposed new owner is no longer an active member.")
	}
	// The new owner is an admin: ownership without the role would be a locked-out owner.
	if err := s.d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		return rolestore.AssignRole(ctx, db, tenant, target.ID, permission.RoleAdmin, nil)
	}); err != nil {
		return nil, rpcError(ctx, err)
	}
	var done *identity.OwnerRecovery
	err = s.d.change(ctx, caller{RC: c.RC, Tenant: tenant, Self: c.Self}, AuditEntityOwner, tenant, audit.ActionUpdate,
		func(ctx context.Context) (map[string][2]any, error) {
			before, _ := identity.OwnerID(ctx, s.d.db(), tenant)
			if err := identity.SetOwner(ctx, s.d.db(), tenant, target.ID); err != nil {
				return nil, err
			}
			var err error
			if done, err = identity.CompleteOwnerRecovery(ctx, s.d.db(), tenant, now); err != nil {
				return nil, err
			}
			return map[string][2]any{"owner": {before.String(), target.ID.String()}}, nil
		})
	if err != nil {
		return nil, ownerErr(ctx, err)
	}
	s.notifyAdmins(ctx, tenant, mail.OwnerRecoveryNotice{NewOwner: target.Email, RequestedBy: done.RequestedByEmail,
		Reason: done.Reason, EffectiveAt: done.EffectiveAt, Completed: true})
	return toPBRecovery(done), nil
}

// AuditEntityOwner is the audit-trail entity owner changes are recorded under (the record is the tenant).
const AuditEntityOwner = "metadata.tenant_owner"

// RequestWorkspaceDeletion lets the owner (and only the owner) delete the workspace (UC-81): it is
// suspended for the retention period, then deleted unless someone cancels with the link every
// admin is emailed. The caller must type the workspace's name and prove it is them: the password,
// and a second-factor code when they have one.
func (s *OwnerService) RequestWorkspaceDeletion(ctx context.Context, req *RequestWorkspaceDeletionRequest) (*WorkspaceDeletion, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.d.ensureOwner(ctx, c.Tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if owner == uuid.Nil || c.Self != owner {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.OwnerProtected, "Only the workspace owner can delete it.")
	}
	name := s.tenantName(ctx, c.Tenant)
	if strings.TrimSpace(req.ConfirmName) != name || name == "" {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "Type the workspace's name to confirm.")
	}
	// Typing the name stops a stray click; it does not stop somebody who holds the owner's session.
	// Deleting the workspace needs the owner's password (and a second-factor code, when they have one).
	if err := s.d.stepUp(ctx, c, req.Password, req.MfaCode); err != nil {
		return nil, err
	}
	now := s.d.now()
	registry := tenancy.NewPostgresTenantRegistry(s.d.cfg.Shared)
	if err := registry.RequestDeletion(ctx, c.Tenant, now, tenancy.DefaultDeletionRetention); err != nil {
		return nil, svcErr(ctx, http.StatusConflict, errcode.ValidationFailed, "A deletion is already scheduled for this workspace.")
	}
	effective := now.Add(tenancy.DefaultDeletionRetention)
	var token string
	err = s.d.change(ctx, c, AuditEntityOwner, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		token, err = identity.CreateDeletionCancelToken(ctx, s.d.db(), c.Tenant, effective)
		return map[string][2]any{"deletionRequested": {false, true}}, err
	})
	if err != nil {
		// Without a way to cancel, do not leave the workspace scheduled.
		_ = registry.CancelDeletion(ctx, c.Tenant, now)
		return nil, rpcError(ctx, err)
	}
	link := s.d.cfg.Mailer.Link("/cancel-deletion", token)
	out := &WorkspaceDeletion{EffectiveAt: formatTime(&effective)}
	sent := 0
	if s.d.cfg.Mailer.Configured() {
		requestedBy := ""
		if m, err := identity.GetMember(ctx, s.d.db(), c.Tenant, c.Self); err == nil {
			requestedBy = m.Email
		}
		if admins, err := s.d.activeAdmins(ctx, c.Tenant); err == nil {
			for _, a := range admins {
				if s.d.cfg.Mailer.SendWorkspaceDeletionNotice(ctx, mail.WorkspaceDeletionNotice{
					To: a.Email, TenantName: name, RequestedBy: requestedBy, EffectiveAt: effective, CancelLink: link,
				}) == nil {
					sent++
				}
			}
		}
	}
	if sent == 0 {
		out.CancelLink = link
	}
	return out, nil
}

// stepUp asks the caller who is already signed in to prove it is them again before something
// that cannot be undone: the current password (a wrong one counts toward the sign-in lockout),
// and a second-factor code when the person has one.
func (d identityDeps) stepUp(ctx context.Context, c caller, password, mfaCode string) error {
	id, err := identity.IdentityOf(ctx, d.cfg.Shared, c.Tenant, c.Self)
	if err != nil {
		return identityErr(ctx, err)
	}
	if password == "" {
		return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: "Enter your password to confirm.", FieldErrors: map[string][]string{"password": {"required"}}})
	}
	switch err := identity.VerifyCurrentPassword(ctx, d.cfg.Shared, id, password, d.now()); {
	case errors.Is(err, identity.ErrInvalidCredentials):
		return svcErr(ctx, http.StatusBadRequest, errcode.WrongPassword, "The password is wrong.")
	case err != nil:
		return identityErr(ctx, err)
	}
	enabled, err := identity.MFAEnabled(ctx, d.cfg.Shared, id)
	if err != nil {
		return rpcError(ctx, err)
	}
	if !enabled {
		return nil
	}
	if mfaCode == "" {
		return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: "Enter your authentication code to confirm.", FieldErrors: map[string][]string{"mfaCode": {"required"}}})
	}
	tx, err := d.cfg.Shared.Begin(ctx)
	if err != nil {
		return rpcError(ctx, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.VerifyMFA(ctx, tx, id, mfaCode, d.now()); err != nil {
		if identity.MFAAttemptCounts(err) {
			_ = tx.Commit(ctx) // keep the wrong-code count
		} else {
			_ = tx.Rollback(ctx)
		}
		return identityErr(ctx, err)
	}
	return rpcError(ctx, tx.Commit(ctx))
}
