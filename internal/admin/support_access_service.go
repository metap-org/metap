package admin

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/tenancy"
)

// AuditEntitySupportGrants is the audit-trail entity support grants are recorded under.
const AuditEntitySupportGrants = "metadata.support_grants"

// SupportAccessService is roadmap 115 M10: time-boxed, reasoned, revocable vendor access.
type SupportAccessService struct {
	d identityDeps
}

func NewSupportAccessService(cfg IdentityConfig) *SupportAccessService {
	return &SupportAccessService{d: identityDeps{cfg: cfg}}
}

func toPBSupportGrant(g identity.SupportGrant, now time.Time) *SupportGrant {
	out := &SupportGrant{Id: g.ID.String(), Reason: g.Reason, Role: g.Role, GrantedBy: g.GrantedBy.String(),
		ExpiresAt: formatTime(&g.ExpiresAt), RevokedAt: formatTime(g.RevokedAt), CreatedAt: formatTime(&g.CreatedAt),
		Active: g.Active(now)}
	for _, s := range g.Sessions {
		started := s.StartedAt
		out.Sessions = append(out.Sessions, &SupportSessionInfo{MemberId: s.MemberID.String(),
			StartedByEmail: s.StartedByEmail, StartedAt: formatTime(&started)})
	}
	return out
}

func supportErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Support grant not found.")
	case errors.Is(err, identity.ErrSupportGrantInactive):
		return svcErr(ctx, http.StatusConflict, errcode.Forbidden, "The tenant has no active support grant.")
	case errors.Is(err, identity.ErrSupportReason):
		return svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	return rpcError(ctx, err)
}

func (s *SupportAccessService) GrantSupportAccess(ctx context.Context, req *GrantSupportAccessRequest) (*SupportGrant, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	if c.Self == uuid.Nil {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "Support access is granted by a person, not a service account.")
	}
	role := req.GetRole()
	if err := s.d.checkRoles(ctx, c.Tenant, []string{role}); err != nil || role == "" {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`role` must be a role of this tenant.")
	}
	now := s.d.now()
	// The grant's id is made by the insert, and the audit entry names it, so both are made in the
	// change; the entry's record id is filled in once the grant exists.
	var g *identity.SupportGrant
	err = s.d.changeNew(ctx, c, AuditEntitySupportGrants, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if g, err = identity.CreateSupportGrant(ctx, s.d.db(), c.Tenant, c.Self, req.GetReason(), role, int(req.GetHours()), now); err != nil {
			return uuid.Nil, nil, supportErr(ctx, err)
		}
		return g.ID, map[string][2]any{
			"reason": {nil, g.Reason}, "role": {nil, g.Role}, "expiresAt": {nil, g.ExpiresAt.UTC().Format(time.RFC3339)}}, nil
	})
	if err != nil {
		return nil, err
	}
	return toPBSupportGrant(*g, now), nil
}

func (s *SupportAccessService) ListSupportGrants(ctx context.Context, _ *Empty) (*ListSupportGrantsResponse, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	now := s.d.now()
	// Lapsed sessions leave no stale "Support" member behind.
	if removed, err := identity.ExpireSupportSessions(ctx, s.d.db(), c.Tenant, now); err == nil {
		s.clearRoles(ctx, c.Tenant, removed)
	}
	grants, err := identity.ListSupportGrants(ctx, s.d.db(), c.Tenant)
	if err != nil {
		return nil, supportErr(ctx, err)
	}
	resp := &ListSupportGrantsResponse{Grants: []*SupportGrant{}}
	for _, g := range grants {
		resp.Grants = append(resp.Grants, toPBSupportGrant(g, now))
	}
	return resp, nil
}

func (s *SupportAccessService) RevokeSupportGrant(ctx context.Context, req *SupportGrantIdRequest) (*SupportGrant, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	now := s.d.now()
	var members []uuid.UUID
	err = s.d.change(ctx, c, AuditEntitySupportGrants, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		if members, err = identity.RevokeSupportGrant(ctx, s.d.db(), c.Tenant, id, now); err != nil {
			return nil, supportErr(ctx, err)
		}
		return map[string][2]any{"revoked": {false, true}, "sessionsEnded": {nil, len(members)}}, nil
	})
	if err != nil {
		return nil, err
	}
	// The roles live in the tenant's own database, outside the change (see identityDeps.change).
	s.clearRoles(ctx, c.Tenant, members)
	g, err := identity.GetSupportGrant(ctx, s.d.db(), c.Tenant, id)
	if err != nil {
		return nil, supportErr(ctx, err)
	}
	return toPBSupportGrant(*g, now), nil
}

// clearRoles drops the roles of removed session members (best effort: the members are already
// deleted and their keys revoked, so they cannot act either way).
func (s *SupportAccessService) clearRoles(ctx context.Context, tenant uuid.UUID, members []uuid.UUID) {
	if len(members) == 0 {
		return
	}
	_ = s.d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		for _, m := range members {
			if err := rolestore.RevokeAllRoles(ctx, db, tenant, m); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SupportAccessService) StartSupportSession(ctx context.Context, req *StartSupportSessionRequest) (*SupportSessionToken, error) {
	c, err := s.d.platformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
	}
	now := s.d.now()
	var grant *identity.SupportGrant
	if req.GetGrantId() != "" {
		gid, err := parseID(req.GetGrantId())
		if err != nil {
			return nil, err
		}
		if grant, err = identity.GetSupportGrant(ctx, s.d.db(), tenant, gid); err != nil {
			return nil, supportErr(ctx, err)
		}
	} else if grant, err = identity.LatestActiveSupportGrant(ctx, s.d.db(), tenant, now); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			err = identity.ErrSupportGrantInactive
		}
		return nil, supportErr(ctx, err)
	}
	if !grant.Active(now) {
		return nil, supportErr(ctx, identity.ErrSupportGrantInactive)
	}
	// The tenant must still be reachable (not suspended or gone).
	if _, err := tenancy.NewPostgresTenantRegistry(s.d.cfg.Shared).Get(ctx, tenant); err != nil {
		return nil, tenantErr(ctx, err)
	}
	person, err := identity.GetMember(ctx, s.d.db(), tenancy.PlatformTenantID, c.Self)
	if err != nil {
		return nil, identityErr(ctx, err)
	}

	expires := now.Add(time.Duration(identity.SupportSessionMaxHours) * time.Hour)
	if grant.ExpiresAt.Before(expires) {
		expires = grant.ExpiresAt
	}
	// The account is made first (its id is needed for the role in the tenant's own database); the key,
	// the session record and the audit entry are one change after it. If anything after the account
	// fails, the account is removed again.
	var member *identity.Member
	var token string
	member, err = identity.CreateServiceAccount(ctx, s.d.db(), tenant, "Support: "+person.Email, nil)
	if err != nil {
		return nil, supportErr(ctx, err)
	}
	fail := func(err error) (*SupportSessionToken, error) {
		_ = identity.RemoveMember(ctx, s.d.db(), tenant, member.ID, now)
		return nil, supportErr(ctx, err)
	}
	if err := s.d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		return rolestore.AssignRole(ctx, db, tenant, member.ID, grant.Role, nil)
	}); err != nil {
		return fail(err)
	}
	// Recorded in the tenant's own trail, under the support person.
	err = s.d.change(ctx, caller{RC: c.RC, Tenant: tenant, Self: c.Self}, AuditEntitySupportGrants, grant.ID, audit.ActionUpdate,
		func(ctx context.Context) (map[string][2]any, error) {
			var key *identity.APIKey
			var err error
			if token, key, err = identity.CreateAPIKey(ctx, s.d.db(), tenant, member.ID, "support session", &expires, nil, now); err != nil {
				return nil, err
			}
			if err := identity.RecordSupportSession(ctx, s.d.db(), identity.SupportSession{GrantID: grant.ID, MemberID: member.ID,
				KeyID: key.ID, StartedBy: c.Self, StartedByEmail: person.Email, StartedAt: now}, tenant); err != nil {
				return nil, err
			}
			return map[string][2]any{"sessionStarted": {nil, person.Email + " as " + grant.Role}}, nil
		})
	if err != nil {
		return fail(err)
	}
	return &SupportSessionToken{Token: token, ExpiresAt: formatTime(&expires), TenantId: tenant.String(),
		Role: grant.Role, GrantId: grant.ID.String()}, nil
}
