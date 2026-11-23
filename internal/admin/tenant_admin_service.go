// TenantAdminService (roadmap 113 P3): registering, renaming, suspending and deleting tenants.
// Platform admins only — a tenant never administers tenants, including itself.
package admin

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// AuditEntityTenants is the audit-trail entity name tenant administration records under.
const AuditEntityTenants = "control.tenants"

const (
	maxTenantNameLen = 200
	maxTierLen       = 40
	defaultTier      = "trial"
)

type TenantAdminService struct {
	d        identityDeps
	registry *tenancy.PostgresTenantRegistry
}

func NewTenantAdminService(cfg IdentityConfig) *TenantAdminService {
	return &TenantAdminService{d: identityDeps{cfg: cfg}, registry: tenancy.NewPostgresTenantRegistry(cfg.Shared)}
}

// platformAdmin authenticates a caller that must be a platform_admin of the platform tenant (and
// hold the admin OAuth2 scope when the token is scoped).
func (s *TenantAdminService) platformAdmin(ctx context.Context) (caller, error) {
	return s.d.platformAdmin(ctx)
}

func (d identityDeps) platformAdmin(ctx context.Context) (caller, error) {
	c, err := d.caller(ctx)
	if err != nil {
		return caller{}, err
	}
	if c.tenant != tenancy.PlatformTenantID || !hasRole(c.rc, RolePlatformAdmin) {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "This action requires the platform_admin role.")
	}
	if !c.rc.ScopePermitsAdminSurface() {
		return caller{}, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "This action requires the `admin` OAuth2 scope.")
	}
	return c, nil
}

func hasRole(rc *permission.RequestContext, role string) bool {
	for _, r := range rc.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func tenantErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, tenancy.ErrTenantNotFound):
		return svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Tenant not found.")
	case errors.Is(err, tenancy.ErrTenantState):
		return svcErr(ctx, http.StatusConflict, errcode.TenantInvalidState, "The tenant is not in a state that allows this.")
	}
	return identityErr(ctx, err)
}

func (s *TenantAdminService) toTenant(ctx context.Context, d tenancy.TenantDetail) (*Tenant, error) {
	var members int32
	if err := s.d.cfg.Shared.QueryRow(ctx,
		`SELECT count(*) FROM metadata.users WHERE tenant_id = $1 AND deleted_at IS NULL`, d.ID).Scan(&members); err != nil {
		return nil, err
	}
	t := &Tenant{
		Id: d.ID.String(), Name: d.Name, Tier: d.Tier, Status: d.Status.String(), CreatedAt: formatTime(&d.CreatedAt),
		TrialExpiresAt: formatTime(d.TrialExpiresAt), DeletionRequestedAt: formatTime(d.DeletionRequestedAt),
		DeletionEffectiveAt: formatTime(d.DeletionEffectiveAt), MemberCount: members,
	}
	if d.Strategy.Kind == tenancy.StrategyDedicatedDb {
		t.Strategy = "dedicated_db"
	} else {
		t.Strategy = "schema"
	}
	if d.Product != nil {
		t.Product = *d.Product
	}
	return t, nil
}

func (s *TenantAdminService) tenant(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	d, err := s.registry.GetDetail(ctx, id)
	if err != nil {
		return nil, tenantErr(ctx, err)
	}
	t, err := s.toTenant(ctx, *d)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	return t, nil
}

func (s *TenantAdminService) record(ctx context.Context, c caller, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	s.d.recordFor(ctx, c, AuditEntityTenants, id, action, diff)
}

func (s *TenantAdminService) ListTenants(ctx context.Context, req *ListTenantsRequest) (*ListTenantsResponse, error) {
	if _, err := s.platformAdmin(ctx); err != nil {
		return nil, err
	}
	rows, total, err := s.registry.Search(ctx, tenancy.TenantFilter{
		Query: req.GetQuery(), Status: req.GetStatus(), Tier: req.GetTier(), Limit: int(req.GetLimit()), Offset: int(req.GetOffset()),
	})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := make([]*Tenant, len(rows))
	for i, d := range rows {
		if out[i], err = s.toTenant(ctx, d); err != nil {
			return nil, rpcError(ctx, err)
		}
	}
	return &ListTenantsResponse{Tenants: out, Total: int32(total)}, nil
}

func (s *TenantAdminService) GetTenant(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	if _, err := s.platformAdmin(ctx); err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	return s.tenant(ctx, id)
}

// CreateTenant registers a tenant on the shared tables and invites its first admin. The tenant
// is visible to Router only once the admin and the admin role exist; any failure on the way
// leaves the tenant deleted, never half-made.
func (s *TenantAdminService) CreateTenant(ctx context.Context, req *CreateTenantRequest) (*CreateTenantResponse, error) {
	c, err := s.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	tier := strings.TrimSpace(req.GetTier())
	if tier == "" {
		tier = defaultTier
	}
	fe := map[string][]string{}
	if name == "" || len(name) > maxTenantNameLen {
		fe["name"] = []string{"A name of 1 to 200 characters is required."}
	}
	if len(tier) > maxTierLen {
		fe["tier"] = []string{"At most 40 characters."}
	}
	if len(fe) > 0 {
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: "Invalid tenant.", FieldErrors: fe})
	}

	now := s.d.now()
	id := uuid.New()
	pool := s.d.cfg.Shared
	if err := s.registry.RegisterShared(ctx, id, name, tier, "provisioning"); err != nil {
		return nil, rpcError(ctx, err)
	}
	fail := func(cause error) error {
		_, _ = s.registry.Deprovision(ctx, id)
		return cause
	}
	if req.GetProduct() != "" {
		if _, err := s.registry.SetProduct(ctx, id, req.GetProduct()); err != nil {
			return nil, fail(rpcError(ctx, err))
		}
	}
	invitedBy := c.selfPtr()
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{
		TenantID: id, Email: req.GetAdminEmail(), DisplayName: req.GetAdminDisplayName(), InvitedBy: invitedBy,
		Status: identity.StatusInvited,
	})
	if err != nil {
		return nil, fail(identityErr(ctx, err))
	}
	if err := rolestore.EnsureBuiltinRoles(ctx, pool, id); err != nil {
		return nil, fail(rpcError(ctx, err))
	}
	if err := rolestore.AssignRole(ctx, pool, id, m.ID, permission.RoleAdmin, invitedBy); err != nil {
		return nil, fail(rpcError(ctx, err))
	}
	newAccount, err := identity.CanSetPasswordOnAccept(ctx, pool, m.IdentityID, m.ID)
	if err != nil {
		return nil, fail(rpcError(ctx, err))
	}
	token, inv, err := identity.CreateInvitation(ctx, pool, id, m.ID, m.IdentityID, newAccount, invitedBy, now)
	if err != nil {
		return nil, fail(rpcError(ctx, err))
	}
	if ok, err := s.registry.SetStatus(ctx, id, "active"); err != nil || !ok {
		return nil, fail(rpcError(ctx, errors.Join(err, errors.New("could not activate the tenant"))))
	}

	mailer := s.d.cfg.Mailer
	link := mailer.Link("/accept-invite", token)
	sendErr := mailer.SendInvitation(ctx, accountmail.Invitation{
		To: m.Email, TenantName: name, InviterName: s.d.displayNameOf(ctx, c),
		Link: link, ExpiresAt: inv.ExpiresAt, NewAccount: newAccount,
	})
	if sendErr != nil && !errors.Is(sendErr, accountmail.ErrNotConfigured) {
		slog.Warn("tenant admin invitation email not sent", "tenant_id", id, standard.LogError, sendErr)
	}
	s.record(ctx, c, id, audit.ActionCreate, map[string][2]any{
		"name": {nil, name}, "tier": {nil, tier}, "adminEmail": {nil, m.Email},
	})
	t, err := s.tenant(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &CreateTenantResponse{Tenant: t, AdminMemberId: m.ID.String(), ExpiresAt: formatTime(&inv.ExpiresAt), EmailSent: sendErr == nil}
	if sendErr != nil {
		resp.AcceptUrl = link
	}
	return resp, nil
}

func (s *TenantAdminService) UpdateTenant(ctx context.Context, req *UpdateTenantRequest) (*Tenant, error) {
	c, err := s.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.registry.GetDetail(ctx, id)
	if err != nil {
		return nil, tenantErr(ctx, err)
	}
	var name, tier *string
	diff := map[string][2]any{}
	if req.Name != nil {
		n := strings.TrimSpace(req.GetName())
		if n == "" || len(n) > maxTenantNameLen {
			return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
				Message: "Invalid tenant.", FieldErrors: map[string][]string{"name": {"A name of 1 to 200 characters is required."}}})
		}
		name = &n
		diff["name"] = [2]any{before.Name, n}
	}
	if req.Tier != nil {
		t := strings.TrimSpace(req.GetTier())
		if t == "" || len(t) > maxTierLen {
			return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
				Message: "Invalid tenant.", FieldErrors: map[string][]string{"tier": {"A tier of 1 to 40 characters is required."}}})
		}
		tier = &t
		diff["tier"] = [2]any{before.Tier, t}
	}
	if err := s.registry.Update(ctx, id, name, tier); err != nil {
		return nil, tenantErr(ctx, err)
	}
	if len(diff) > 0 {
		s.record(ctx, c, id, audit.ActionUpdate, diff)
	}
	return s.tenant(ctx, id)
}

// lifecycle runs one state change and returns the tenant afterwards.
func (s *TenantAdminService) lifecycle(ctx context.Context, idStr, change string,
	fn func(id uuid.UUID, now time.Time) error, diff map[string][2]any) (*Tenant, error) {
	c, err := s.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(idStr)
	if err != nil {
		return nil, err
	}
	if err := fn(id, s.d.now()); err != nil {
		return nil, tenantErr(ctx, err)
	}
	diff["change"] = [2]any{nil, change}
	s.record(ctx, c, id, audit.ActionUpdate, diff)
	return s.tenant(ctx, id)
}

func (s *TenantAdminService) SuspendTenant(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "suspend", func(id uuid.UUID, _ time.Time) error {
		return s.registry.Suspend(ctx, id)
	}, map[string][2]any{"status": {"active", "suspended"}})
}

func (s *TenantAdminService) ResumeTenant(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "resume", func(id uuid.UUID, _ time.Time) error {
		return s.registry.Resume(ctx, id)
	}, map[string][2]any{"status": {"suspended", "active"}})
}

func (s *TenantAdminService) RequestTenantDeletion(ctx context.Context, req *RequestTenantDeletionRequest) (*Tenant, error) {
	retention := time.Duration(req.GetRetentionDays()) * 24 * time.Hour
	return s.lifecycle(ctx, req.GetId(), "request-deletion", func(id uuid.UUID, now time.Time) error {
		return s.registry.RequestDeletion(ctx, id, now, retention)
	}, map[string][2]any{"status": {nil, "suspended"}, "deletionRequested": {false, true}})
}

func (s *TenantAdminService) CancelTenantDeletion(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "cancel-deletion", func(id uuid.UUID, now time.Time) error {
		return s.registry.CancelDeletion(ctx, id, now)
	}, map[string][2]any{"deletionRequested": {true, false}})
}
