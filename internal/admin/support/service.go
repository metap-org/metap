// Package support is roadmap 115 M10 as an administrative slice: time-boxed, reasoned, revocable
// vendor access. A tenant admin grants a role for a while (with a reason); a platform admin then
// starts a session under that grant, which is a service account in the tenant holding exactly the
// granted role plus a key that ends with the grant. types.go, rules.go (the wire form, the session's
// end, the error mapping), store.go (the port: grants, the account, the key, the role in the tenant's
// own database) with its Postgres adapter, and this file, which only orchestrates.
package support

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/tenancy"
)

type Service struct {
	store Store
	deps  kernel.Deps
}

// New is the service over store; deps carry the clock, the audit sink and the caller checks.
func New(store Store, deps kernel.Deps) *Service { return &Service{store: store, deps: deps} }

// Grant lets platform support in for req.Hours with the given role, for a reason. A person grants
// it, not a service account.
func (s *Service) Grant(ctx context.Context, req *GrantSupportAccessRequest) (*SupportGrant, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	if c.Self == uuid.Nil {
		return nil, kernel.Err(http.StatusForbidden, errcode.Forbidden, "Support access is granted by a person, not a service account.")
	}
	role := req.Role
	known, err := s.store.KnownRoles(ctx, c.Tenant)
	if err != nil {
		return nil, err
	}
	if kernel.UnknownRoles(known, []string{role}) != nil || role == "" {
		return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, "`role` must be a role of this tenant.")
	}
	now := s.deps.Now()
	// The grant's id is made by the insert, and the audit entry names it, so both are made in the
	// change; the entry's record id is filled in once the grant exists.
	var g *identity.SupportGrant
	err = s.deps.ChangeNew(ctx, c, AuditEntity, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if g, err = s.store.CreateGrant(ctx, c.Tenant, c.Self, req.Reason, role, int(req.Hours), now); err != nil {
			return uuid.Nil, nil, mapErr(err)
		}
		return g.ID, map[string][2]any{
			"reason": {nil, g.Reason}, "role": {nil, g.Role}, "expiresAt": {nil, g.ExpiresAt.UTC().Format(time.RFC3339)}}, nil
	})
	if err != nil {
		return nil, err
	}
	return view(*g, now), nil
}

// List is the tenant's grants and the sessions under them. Lapsed sessions are swept first, so none
// leaves a stale "Support" member behind.
func (s *Service) List(ctx context.Context) (*ListSupportGrantsResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	now := s.deps.Now()
	if removed, err := s.store.ExpireSessions(ctx, c.Tenant, now); err == nil {
		s.clearRoles(ctx, c.Tenant, removed)
	}
	grants, err := s.store.ListGrants(ctx, c.Tenant)
	if err != nil {
		return nil, mapErr(err)
	}
	resp := &ListSupportGrantsResponse{Grants: []*SupportGrant{}}
	for _, g := range grants {
		resp.Grants = append(resp.Grants, view(g, now))
	}
	return resp, nil
}

// Revoke ends a grant at once: its keys stop working and its session members are removed.
func (s *Service) Revoke(ctx context.Context, req *SupportGrantIdRequest) (*SupportGrant, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.Id)
	if err != nil {
		return nil, err
	}
	now := s.deps.Now()
	var members []uuid.UUID
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		if members, err = s.store.RevokeGrant(ctx, c.Tenant, id, now); err != nil {
			return nil, mapErr(err)
		}
		return map[string][2]any{"revoked": {false, true}, "sessionsEnded": {nil, len(members)}}, nil
	})
	if err != nil {
		return nil, err
	}
	// The roles live in the tenant's own database, outside the change (see Store).
	s.clearRoles(ctx, c.Tenant, members)
	g, err := s.store.GetGrant(ctx, c.Tenant, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return view(*g, now), nil
}

// clearRoles drops the roles of removed session members (best effort: the members are already
// deleted and their keys revoked, so they cannot act either way).
func (s *Service) clearRoles(ctx context.Context, tenant uuid.UUID, members []uuid.UUID) {
	if len(members) == 0 {
		return
	}
	_ = s.store.ClearRoles(ctx, tenant, members)
}

// Start opens a support session in a tenant under its newest active grant (or the one named): a
// platform admin only. The answer carries the session's key once.
func (s *Service) Start(ctx context.Context, req *StartSupportSessionRequest) (*SupportSessionToken, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
	}
	now := s.deps.Now()
	var grant *identity.SupportGrant
	if req.GrantId != "" {
		gid, err := kernel.ParseID(req.GrantId)
		if err != nil {
			return nil, err
		}
		if grant, err = s.store.GetGrant(ctx, tenant, gid); err != nil {
			return nil, mapErr(err)
		}
	} else if grant, err = s.store.LatestActiveGrant(ctx, tenant, now); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			err = identity.ErrSupportGrantInactive
		}
		return nil, mapErr(err)
	}
	if !grant.Active(now) {
		return nil, mapErr(identity.ErrSupportGrantInactive)
	}
	// The tenant must still be reachable (not suspended or gone).
	if err := s.store.TenantReachable(ctx, tenant); err != nil {
		return nil, kernel.TenantErr(err)
	}
	person, err := s.store.Person(ctx, tenancy.PlatformTenantID, c.Self)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}

	expires := sessionExpiry(grant.ExpiresAt, now)
	// The account is made first (its id is needed for the role in the tenant's own database); the key,
	// the session record and the audit entry are one change after it. If anything after the account
	// fails, the account is removed again.
	member, err := s.store.CreateAccount(ctx, tenant, "Support: "+person.Email)
	if err != nil {
		return nil, mapErr(err)
	}
	fail := func(err error) (*SupportSessionToken, error) {
		_ = s.store.RemoveMember(ctx, tenant, member.ID, now)
		return nil, mapErr(err)
	}
	if err := s.store.AssignRole(ctx, tenant, member.ID, grant.Role); err != nil {
		return fail(err)
	}
	// Recorded in the tenant's own trail, under the support person.
	var token string
	err = s.deps.Change(ctx, kernel.Caller{RC: c.RC, Tenant: tenant, Self: c.Self}, AuditEntity, grant.ID, audit.ActionUpdate,
		func(ctx context.Context) (map[string][2]any, error) {
			var key *identity.APIKey
			var err error
			if token, key, err = s.store.CreateKey(ctx, tenant, member.ID, "support session", expires, now); err != nil {
				return nil, err
			}
			if err := s.store.RecordSession(ctx, tenant, identity.SupportSession{GrantID: grant.ID, MemberID: member.ID,
				KeyID: key.ID, StartedBy: c.Self, StartedByEmail: person.Email, StartedAt: now}); err != nil {
				return nil, err
			}
			return map[string][2]any{"sessionStarted": {nil, person.Email + " as " + grant.Role}}, nil
		})
	if err != nil {
		return fail(err)
	}
	return &SupportSessionToken{Token: token, ExpiresAt: kernel.FormatTime(&expires), TenantId: tenant.String(),
		Role: grant.Role, GrantId: grant.ID.String()}, nil
}
