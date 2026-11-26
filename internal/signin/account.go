package signin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// InvitationView is what the accept page needs to know about an open invitation.
type InvitationView struct {
	Email       string
	DisplayName string
	TenantID    uuid.UUID
	TenantName  string
	ExpiresAt   time.Time
	// PasswordMode: "set" (choose a password), "confirm" (type the current one) or "provider"
	// (sign in elsewhere).
	PasswordMode      string
	MinPasswordLength int
}

// LookupInvitation tells the accept page what to ask for: a new password (a brand-new account) or
// the current one (an existing account), and the policy it must meet.
func (s *Service) LookupInvitation(ctx context.Context, token string) (*InvitationView, error) {
	inv, err := s.store.FindInvitation(ctx, strings.TrimSpace(token), s.now())
	if err != nil {
		return nil, InvitationError(err)
	}
	setsPassword, err := s.store.CanSetPasswordOnAccept(ctx, inv.IdentityID, inv.MemberID)
	if err != nil {
		return nil, err
	}
	minLength, err := s.store.RequiredPasswordLength(ctx, inv.IdentityID, inv.TenantID)
	if err != nil {
		return nil, err
	}
	return &InvitationView{Email: inv.Email, DisplayName: inv.DisplayName, TenantID: inv.TenantID, TenantName: inv.TenantName,
		ExpiresAt: inv.ExpiresAt, PasswordMode: passwordMode(setsPassword, inv.HasPassword), MinPasswordLength: minLength}, nil
}

// AcceptInvitation joins the tenant and signs the person in. A brand-new account sets its password
// here; an existing one proves it is its owner with its current password (wrong attempts count
// toward the lockout like a sign-in), because the inviting admin can see the link too.
func (s *Service) AcceptInvitation(ctx context.Context, token, password, displayName string, c Client) (*Grant, error) {
	now := s.now()
	inv, err := s.store.FindInvitation(ctx, strings.TrimSpace(token), now)
	if err != nil {
		return nil, InvitationError(err)
	}
	setsPassword, err := s.store.CanSetPasswordOnAccept(ctx, inv.IdentityID, inv.MemberID)
	if err != nil {
		return nil, err
	}
	switch {
	case setsPassword:
		if err := s.store.CheckPassword(ctx, inv.IdentityID, password, inv.TenantID); err != nil {
			return nil, InvitationError(err)
		}
	case inv.HasPassword:
		ident, err := s.store.Authenticate(ctx, inv.Email, password, now)
		if err != nil {
			return nil, IdentityError(err)
		}
		if ident.ID != inv.IdentityID {
			return nil, InvitationError(identity.ErrInvitationInvalid)
		}
	default:
		return nil, InvitationError(identity.ErrInvitationNeedsSignIn)
	}

	if err := s.store.AcceptInvitation(ctx, inv, setsPassword, password, strings.TrimSpace(displayName), now); err != nil {
		return nil, InvitationError(err)
	}

	memberships, err := s.store.Memberships(ctx, inv.IdentityID)
	if err != nil {
		return nil, err
	}
	for _, m := range memberships {
		if m.MemberID != inv.MemberID {
			continue
		}
		// A tenant that signs in through its identity provider must not hand out a password
		// session here. The person has joined; they continue through the provider.
		if err := s.RequirePasswordAllowed(ctx, m); err != nil {
			return nil, err
		}
		// SetPassword revoked earlier sessions; this one is minted after, so it survives.
		return s.Issue(ctx, inv.IdentityID, m, c)
	}
	// Joined, but the tenant itself is not active (suspended): nothing to sign in to yet.
	return nil, serviceErr(http.StatusForbidden, errcode.NoTenantAccess, "Invitation accepted, but this tenant is not active right now.", nil)
}

// RequestPasswordReset emails a reset link to the account at email, if there is an active one. It
// reports nothing: the HTTP answer must be the same either way, so neither it nor its timing tells
// whether an account exists (run it in the background). At most identity.MaxPasswordResetsPerHour
// links per account and hour.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) {
	now := s.now()
	ident, err := s.store.FindIdentityByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if !errors.Is(err, identity.ErrNotFound) {
			slog.Error("password reset: looking up identity", standard.LogError, err)
		}
		return
	}
	if ident.Status != identity.StatusActive {
		return
	}
	token, err := s.store.CreatePasswordReset(ctx, ident.ID, now)
	if err != nil {
		slog.Error("password reset: issuing link", standard.LogError, err)
		return
	}
	if token == "" {
		slog.Warn("password reset: hourly limit reached", "identity_id", ident.ID)
		return
	}
	if err := s.d.Mailer.SendPasswordReset(ctx, mail.PasswordReset{
		To: ident.Email, Link: s.d.Mailer.Link("/reset-password", token), ExpiresAt: now.Add(identity.PasswordResetTTL),
	}); err != nil {
		slog.Error("password reset: sending email", "identity_id", ident.ID, standard.LogError, err)
	}
}

// ResetPassword sets the new password from a reset link. It signs nobody in (every session of the
// account is revoked): the person signs in with the new password next.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	err := s.store.ResetPassword(ctx, strings.TrimSpace(token), password, s.now(),
		func(person uuid.UUID) error { return s.store.CheckPassword(ctx, person, password, uuid.Nil) })
	if err != nil {
		return InvitationError(err)
	}
	return nil
}

// CancelWorkspaceDeletion: the emailed link's token reopens a workspace whose owner asked to delete
// it (UC-81). A token works once and only until the deletion would have taken effect.
func (s *Service) CancelWorkspaceDeletion(ctx context.Context, token string) error {
	now := s.now()
	tenant, err := s.store.ConsumeDeletionCancelToken(ctx, strings.TrimSpace(token), now)
	if errors.Is(err, identity.ErrCancelInvalid) {
		return serviceErr(http.StatusBadRequest, errcode.ValidationFailed, err.Error(), nil)
	}
	if err != nil {
		slog.Error("cancel workspace deletion: using token", standard.LogError, err)
		return serviceErr(http.StatusInternalServerError, errcode.InternalError, "Something went wrong.", nil)
	}
	if err := s.store.CancelDeletion(ctx, tenant, now); err != nil {
		// Already cancelled by a platform admin, or past the point of no return: the link is spent
		// either way, and saying "invalid" matches what the person would find.
		return serviceErr(http.StatusBadRequest, errcode.ValidationFailed, identity.ErrCancelInvalid.Error(), nil)
	}
	return nil
}
