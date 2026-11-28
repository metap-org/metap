// Package owner is a tenant owner who is more than an admin, and a recovery path the tenant can see
// coming and cancel (roadmap 115 M8), plus deleting the workspace (UC-81), as an administrative slice:
// types.go, rules.go, store.go with its Postgres adapter, and this file. Ensure is what every other
// slice that asks "who owns this tenant" calls: the owner is assigned lazily, to the oldest active
// admin, the first time anything asks.
package owner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/tenancy"

	"net/http"
)

type Service struct {
	store  Store
	notify Notifier
	deps   kernel.Deps
}

// New is the service over store. notify emails the tenant's admins (nil sends nothing); deps carry the
// clock, the audit sink and the caller checks.
func New(store Store, notify Notifier, deps kernel.Deps) *Service {
	return &Service{store: store, notify: notify, deps: deps}
}

// Ensure returns the tenant's owner, assigning the oldest active admin when none is set yet (tenants
// that predate owners, and brand-new ones, are covered the first time anything asks). uuid.Nil: no
// owner and no admin to make one.
func Ensure(ctx context.Context, s Store, tenant uuid.UUID) (uuid.UUID, error) {
	owner, err := s.OwnerID(ctx, tenant)
	if err != nil || owner != uuid.Nil {
		return owner, err
	}
	admins, err := s.ActiveAdmins(ctx, tenant)
	if err != nil || len(admins) == 0 {
		return uuid.Nil, err
	}
	first := oldest(admins)
	if err := s.SetOwner(ctx, tenant, first.ID); err != nil {
		return uuid.Nil, err
	}
	return first.ID, nil
}

// Guard refuses an action on the owner by anyone but the owner.
func Guard(ctx context.Context, s Store, c kernel.Caller, target uuid.UUID, what string) error {
	owner, err := Ensure(ctx, s, c.Tenant)
	if err != nil {
		return err
	}
	if owner != uuid.Nil && target == owner && c.Self != owner {
		return fail(http.StatusForbidden, errcode.OwnerProtected, "Only the tenant owner can "+what+" the owner.")
	}
	return nil
}

func (s *Service) status(ctx context.Context, tenant uuid.UUID) (*OwnerStatus, error) {
	owner, err := Ensure(ctx, s.store, tenant)
	if err != nil {
		return nil, err
	}
	out := &OwnerStatus{}
	if owner != uuid.Nil {
		out.OwnerMemberId = owner.String()
		if m, err := s.store.Member(ctx, tenant, owner); err == nil {
			out.OwnerEmail = m.Email
		}
	}
	if r, err := s.store.PendingRecovery(ctx, tenant); err == nil {
		out.PendingRecovery = recoveryView(r)
	} else if !errors.Is(err, identity.ErrNoRecovery) {
		return nil, err
	}
	return out, nil
}

func (s *Service) GetStatus(ctx context.Context, _ *kernel.Empty) (*OwnerStatus, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, c.Tenant)
}

// PlatformTenantOwner is GetStatus for a platform admin on any tenant, plus the active people who could
// take over. Read-only: unlike the tenant's own view it does not name a default owner when none is set
// (that absence is exactly the case a recovery exists for).
func (s *Service) PlatformTenantOwner(ctx context.Context, req *TenantIdOnlyRequest) (*TenantOwnerView, error) {
	if _, err := s.deps.PlatformAdmin(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, tenantIDErr()
	}
	owner, err := s.store.OwnerID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	view := &TenantOwnerView{Owner: &OwnerStatus{}, People: []*TenantPerson{}}
	if owner != uuid.Nil {
		view.Owner.OwnerMemberId = owner.String()
	}
	if r, err := s.store.PendingRecovery(ctx, tenant); err == nil {
		view.Owner.PendingRecovery = recoveryView(r)
	} else if !errors.Is(err, identity.ErrNoRecovery) {
		return nil, err
	}
	people, err := s.store.ActivePeople(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, m := range people {
		isOwner := m.ID == owner
		if isOwner {
			view.Owner.OwnerEmail = m.Email
		}
		view.People = append(view.People, &TenantPerson{Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName, IsOwner: isOwner})
	}
	return view, nil
}

func (s *Service) Transfer(ctx context.Context, req *TransferOwnershipRequest) (*OwnerStatus, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := Ensure(ctx, s.store, c.Tenant)
	if err != nil {
		return nil, err
	}
	if owner == uuid.Nil || c.Self != owner {
		return nil, fail(http.StatusForbidden, errcode.OwnerProtected, "Only the tenant owner can transfer ownership.")
	}
	target, err := kernel.ParseID(req.MemberId)
	if err != nil {
		return nil, err
	}
	if target == owner {
		return s.status(ctx, c.Tenant)
	}
	admins, err := s.store.ActiveAdmins(ctx, c.Tenant)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(admins, func(m identity.Member) bool { return m.ID == target }) {
		return nil, fail(http.StatusBadRequest, errcode.ValidationFailed, "The new owner must be an active admin of this tenant.")
	}
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		return map[string][2]any{"owner": {owner.String(), target.String()}}, s.store.SetOwner(ctx, c.Tenant, target)
	})
	if err != nil {
		return nil, storeErr(err)
	}
	return s.status(ctx, c.Tenant)
}

func (s *Service) CancelRecovery(ctx context.Context, _ *kernel.Empty) (*OwnerStatus, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		r, err := s.store.CancelRecovery(ctx, c.Tenant, c.Self, s.deps.Now())
		if err != nil {
			return nil, err
		}
		return map[string][2]any{"ownerRecovery": {"pending for " + r.NewOwnerMember.String(), "cancelled"}}, nil
	})
	if err != nil {
		return nil, storeErr(err)
	}
	return s.status(ctx, c.Tenant)
}

// notifyAdmins emails every active admin of the tenant (best effort: the recovery stands either way).
func (s *Service) notifyAdmins(ctx context.Context, tenant uuid.UUID, n mail.OwnerRecoveryNotice) {
	if s.notify == nil {
		return
	}
	admins, err := s.store.ActiveAdmins(ctx, tenant)
	if err != nil {
		return
	}
	n.TenantName = s.store.TenantName(ctx, tenant)
	for _, a := range admins {
		n.To = a.Email
		_ = s.notify.SendOwnerRecoveryNotice(ctx, n)
	}
}

func (s *Service) RequestRecovery(ctx context.Context, req *RequestOwnerRecoveryRequest) (*OwnerRecovery, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, tenantIDErr()
	}
	newOwner, err := kernel.ParseID(req.NewOwnerMemberId)
	if err != nil {
		return nil, err
	}
	target, err := s.store.Member(ctx, tenant, newOwner)
	if err != nil || target.Status != identity.StatusActive || target.AuthProvider == identity.ServiceAccountProvider {
		return nil, fail(http.StatusBadRequest, errcode.ValidationFailed, "The new owner must be an active person in the tenant.")
	}
	person, err := s.store.Member(ctx, tenancy.PlatformTenantID, c.Self)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	now := s.deps.Now()
	var r *identity.OwnerRecovery
	// The change is made in the tenant being recovered, by the platform admin.
	in := kernel.Caller{RC: c.RC, Tenant: tenant, Self: c.Self}
	err = s.deps.Change(ctx, in, AuditEntity, tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		if r, err = s.store.RequestRecovery(ctx, tenant, newOwner, c.Self, person.Email, req.Reason, now); err != nil {
			return nil, err
		}
		return map[string][2]any{"ownerRecovery": {nil, "requested for " + target.Email + " (effective " + r.EffectiveAt.UTC().Format(time.RFC3339) + ")"}}, nil
	})
	if err != nil {
		return nil, storeErr(err)
	}
	s.notifyAdmins(ctx, tenant, mail.OwnerRecoveryNotice{NewOwner: target.Email, RequestedBy: person.Email,
		Reason: r.Reason, EffectiveAt: r.EffectiveAt})
	return recoveryView(r), nil
}

func (s *Service) CompleteRecovery(ctx context.Context, req *TenantIdOnlyRequest) (*OwnerRecovery, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, tenantIDErr()
	}
	pending, err := s.store.PendingRecovery(ctx, tenant)
	if err != nil {
		return nil, storeErr(err)
	}
	now := s.deps.Now()
	if now.Before(pending.EffectiveAt) {
		return nil, storeErr(identity.ErrRecoveryNotReady)
	}
	target, err := s.store.Member(ctx, tenant, pending.NewOwnerMember)
	if err != nil || target.Status != identity.StatusActive {
		return nil, fail(http.StatusConflict, errcode.ValidationFailed, "The proposed new owner is no longer an active member.")
	}
	// The new owner is an admin: ownership without the role would be a locked-out owner. The role lives
	// in the tenant's own database, so it is granted before (not inside) the change on the shared one.
	if err := s.store.GrantAdmin(ctx, tenant, target.ID); err != nil {
		return nil, err
	}
	var done *identity.OwnerRecovery
	in := kernel.Caller{RC: c.RC, Tenant: tenant, Self: c.Self}
	err = s.deps.Change(ctx, in, AuditEntity, tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		before, _ := s.store.OwnerID(ctx, tenant)
		if err := s.store.SetOwner(ctx, tenant, target.ID); err != nil {
			return nil, err
		}
		var err error
		if done, err = s.store.CompleteRecovery(ctx, tenant, now); err != nil {
			return nil, err
		}
		return map[string][2]any{"owner": {before.String(), target.ID.String()}}, nil
	})
	if err != nil {
		return nil, storeErr(err)
	}
	s.notifyAdmins(ctx, tenant, mail.OwnerRecoveryNotice{NewOwner: target.Email, RequestedBy: done.RequestedByEmail,
		Reason: done.Reason, EffectiveAt: done.EffectiveAt, Completed: true})
	return recoveryView(done), nil
}

// RequestWorkspaceDeletion lets the owner (and only the owner) delete the workspace (UC-81): it is
// suspended for the retention period, then deleted unless someone cancels with the link every admin is
// emailed. The caller must type the workspace's name and prove it is them: the password, and a
// second-factor code when they have one.
func (s *Service) RequestWorkspaceDeletion(ctx context.Context, req *RequestWorkspaceDeletionRequest) (*WorkspaceDeletion, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := Ensure(ctx, s.store, c.Tenant)
	if err != nil {
		return nil, err
	}
	if owner == uuid.Nil || c.Self != owner {
		return nil, fail(http.StatusForbidden, errcode.OwnerProtected, "Only the workspace owner can delete it.")
	}
	name := s.store.TenantName(ctx, c.Tenant)
	if strings.TrimSpace(req.ConfirmName) != name || name == "" {
		return nil, fail(http.StatusBadRequest, errcode.ValidationFailed, "Type the workspace's name to confirm.")
	}
	// Typing the name stops a stray click; it does not stop somebody who holds the owner's session.
	// Deleting the workspace needs the owner's password (and a second-factor code, when they have one).
	if err := s.stepUp(ctx, c, req.Password, req.MfaCode); err != nil {
		return nil, err
	}
	now := s.deps.Now()
	if err := s.store.ScheduleDeletion(ctx, c.Tenant, now, tenancy.DefaultDeletionRetention); err != nil {
		return nil, fail(http.StatusConflict, errcode.ValidationFailed, "A deletion is already scheduled for this workspace.")
	}
	effective := now.Add(tenancy.DefaultDeletionRetention)
	var token string
	err = s.deps.Change(ctx, c, AuditEntity, c.Tenant, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		token, err = s.store.CancelToken(ctx, c.Tenant, effective)
		return map[string][2]any{"deletionRequested": {false, true}}, err
	})
	if err != nil {
		// Without a way to cancel, do not leave the workspace scheduled.
		_ = s.store.CancelDeletion(ctx, c.Tenant, now)
		return nil, err
	}
	link := (*mail.Mailer)(nil).Link("/cancel-deletion", token)
	if m, ok := s.notify.(interface {
		Link(path, token string) string
	}); ok {
		link = m.Link("/cancel-deletion", token)
	}
	out := &WorkspaceDeletion{EffectiveAt: kernel.FormatTime(&effective)}
	sent := 0
	if s.notify != nil && s.notify.Configured() {
		requestedBy := ""
		if m, err := s.store.Member(ctx, c.Tenant, c.Self); err == nil {
			requestedBy = m.Email
		}
		if admins, err := s.store.ActiveAdmins(ctx, c.Tenant); err == nil {
			for _, a := range admins {
				if s.notify.SendWorkspaceDeletionNotice(ctx, mail.WorkspaceDeletionNotice{
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

// stepUp asks the caller who is already signed in to prove it is them again before something that
// cannot be undone: the current password (a wrong one counts toward the sign-in lockout), and a
// second-factor code when the person has one.
func (s *Service) stepUp(ctx context.Context, c kernel.Caller, password, mfaCode string) error {
	id, err := s.store.IdentityOf(ctx, c.Tenant, c.Self)
	if err != nil {
		return kernel.IdentityErr(err)
	}
	if password == "" {
		return fieldRequired("password", "Enter your password to confirm.")
	}
	if err := s.store.VerifyPassword(ctx, id, password, s.deps.Now()); err != nil {
		return stepUpErr(err)
	}
	enabled, err := s.store.MFAEnabled(ctx, id)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if mfaCode == "" {
		return fieldRequired("mfaCode", "Enter your authentication code to confirm.")
	}
	return kernelIdentity(s.store.VerifyMFA(ctx, id, mfaCode, s.deps.Now()))
}

func kernelIdentity(err error) error {
	if err == nil {
		return nil
	}
	return kernel.IdentityErr(err)
}
