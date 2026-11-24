package admin

import (
	"net/http"

	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// A person's own two-factor settings are internal/admin/mfa; this is the admin resetting someone
// else's, which needs the owner and shared-identity guards of user administration.
func (s *UserAdminService) ResetMfa(ctx context.Context, req *UserIdRequest) (*Empty, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.d.guardOwner(ctx, c, id, "reset the MFA of"); err != nil {
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
			"This person also belongs to another tenant; only they can change their two-factor settings.")
	}
	if err := identity.DisableMFA(ctx, s.d.cfg.Shared, m.IdentityID); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"mfa": {"on", "reset"}})
	return &Empty{}, nil
}
