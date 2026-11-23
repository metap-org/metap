// Invitations (roadmap 113 P2): an admin adds someone as an invited member and they get an email
// link; accepting it (REST /auth/invitations/accept, it sets the session cookie) activates them.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/peripherals"
)

func (s *UserAdminService) InviteUser(ctx context.Context, req *InviteUserRequest) (*InviteUserResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	roles := dedupe(req.GetRoles())
	if err := s.d.checkRoles(ctx, c.tenant, roles); err != nil {
		return nil, err
	}
	invitedBy := c.selfPtr()
	m, _, err := identity.AddMember(ctx, s.d.cfg.Shared, identity.NewMember{
		TenantID: c.tenant, Email: req.GetEmail(), DisplayName: req.GetDisplayName(), InvitedBy: invitedBy,
		Status: identity.StatusInvited,
	})
	if err != nil {
		return nil, identityErr(ctx, err)
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
			_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, m.ID, s.d.now())
			return nil, rpcError(ctx, err)
		}
	}
	resp, err := s.issueInvitation(ctx, c, m.ID)
	if err != nil {
		_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, m.ID, s.d.now())
		return nil, err
	}
	s.d.record(ctx, c, m.ID, audit.ActionCreate, map[string][2]any{
		"email": {nil, m.Email}, "displayName": {nil, m.DisplayName}, "roles": {nil, roles},
		"status": {nil, identity.StatusInvited},
	})
	return resp, nil
}

func (s *UserAdminService) ResendInvitation(ctx context.Context, req *UserIdRequest) (*InviteUserResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	resp, err := s.issueInvitation(ctx, c, id)
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"invitation": {nil, "resent"}})
	return resp, nil
}

// issueInvitation creates a fresh link for invited member id and emails it. A relay failure is not
// an error: the invitation stands and the link comes back for the admin to pass on.
func (s *UserAdminService) issueInvitation(ctx context.Context, c caller, id uuid.UUID) (*InviteUserResponse, error) {
	m, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if m.Status != identity.StatusInvited {
		return nil, svcErr(ctx, http.StatusConflict, errcode.AlreadyMember, "This person has already joined the tenant.")
	}
	now := s.d.now()
	newAccount, err := identity.CanSetPasswordOnAccept(ctx, s.d.cfg.Shared, m.IdentityID, m.ID)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	token, inv, err := identity.CreateInvitation(ctx, s.d.cfg.Shared, c.tenant, m.ID, m.IdentityID, newAccount, c.selfPtr(), now)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	mailer := s.d.cfg.Mailer
	link := mailer.Link("/accept-invite", token)
	sendErr := mailer.SendInvitation(ctx, accountmail.Invitation{
		To: m.Email, TenantName: inv.TenantName, InviterName: s.d.displayNameOf(ctx, c),
		Link: link, ExpiresAt: inv.ExpiresAt, NewAccount: newAccount,
	})
	if sendErr != nil && !errors.Is(sendErr, accountmail.ErrNotConfigured) {
		slog.Warn("invitation email not sent", "member_id", m.ID, standard.LogError, sendErr)
	}
	u, err := s.d.user(ctx, c.tenant, m.ID)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	resp := &InviteUserResponse{User: u, ExpiresAt: formatTime(&inv.ExpiresAt), EmailSent: sendErr == nil}
	if sendErr != nil {
		resp.AcceptUrl = link
	}
	return resp, nil
}

// displayNameOf is how the caller signs an invitation: their display name, else their email.
func (d identityDeps) displayNameOf(ctx context.Context, c caller) string {
	if c.self == uuid.Nil {
		return ""
	}
	m, err := identity.GetMember(ctx, d.cfg.Shared, c.tenant, c.self)
	if err != nil {
		return ""
	}
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Email
}

func (c caller) selfPtr() *uuid.UUID {
	if c.self == uuid.Nil {
		return nil
	}
	self := c.self
	return &self
}
