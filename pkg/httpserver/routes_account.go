// Public account self-service (roadmap 113 P2): accepting an invitation and resetting a forgotten
// password. REST, like login, because accepting signs the person in (sets the session cookie) and
// none of it has a session to authenticate a gRPC/GraphQL call with. Tokens travel in POST bodies,
// never in a URL this server logs.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
)

type tokenBody struct {
	Token string `json:"token"`
}

func writeInvitationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvitationInvalid):
		writeServiceError(w, http.StatusBadRequest, errcode.InvitationInvalid, err.Error(), nil)
	case errors.Is(err, identity.ErrInvitationNeedsSignIn):
		writeServiceError(w, http.StatusConflict, errcode.InvitationNeedsSignIn, err.Error(), nil)
	case errors.Is(err, identity.ErrResetInvalid):
		writeServiceError(w, http.StatusBadRequest, errcode.ResetTokenInvalid, err.Error(), nil)
	case errors.Is(err, identity.ErrWeakPassword):
		writeServiceError(w, http.StatusBadRequest, errcode.WeakPassword, err.Error(),
			map[string][]string{"password": {err.Error()}})
	default:
		writeIdentityError(w, err)
	}
}

// lookupInvitation tells the accept page what to ask for: a new password (a brand-new account) or
// the current one (an existing account), and the policy it must meet.
func lookupInvitation(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body tokenBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	inv, err := identity.FindInvitation(r.Context(), state.Pool, strings.TrimSpace(body.Token), time.Now())
	if err != nil {
		writeInvitationError(w, err)
		return
	}
	setsPassword, err := identity.CanSetPasswordOnAccept(r.Context(), state.Pool, inv.IdentityID, inv.MemberID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	minLength, err := identity.RequiredPasswordLength(r.Context(), state.Pool, inv.IdentityID, state.PasswordPolicy(), inv.TenantID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"email": inv.Email, "displayName": inv.DisplayName, "tenantId": inv.TenantID,
		"tenantName": nullableString(inv.TenantName), "expiresAt": inv.ExpiresAt.UTC().Format(time.RFC3339),
		// "set": choose a password; "confirm": type the current one; "provider": sign in elsewhere.
		"passwordMode": passwordMode(setsPassword, inv.HasPassword), "minPasswordLength": minLength,
	}})
}

func passwordMode(setsPassword, hasPassword bool) string {
	switch {
	case setsPassword:
		return "set"
	case hasPassword:
		return "confirm"
	default:
		return "provider"
	}
}

type acceptInvitationBody struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// acceptInvitation joins the tenant and signs the person in. A brand-new account sets its password
// here; an existing one proves it is its owner with its current password (wrong attempts count
// toward the lockout like a sign-in), because the inviting admin can see the link too.
func acceptInvitation(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body acceptInvitationBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx, now := r.Context(), time.Now()
	inv, err := identity.FindInvitation(ctx, state.Pool, strings.TrimSpace(body.Token), now)
	if err != nil {
		writeInvitationError(w, err)
		return
	}
	setsPassword, err := identity.CanSetPasswordOnAccept(ctx, state.Pool, inv.IdentityID, inv.MemberID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	switch {
	case setsPassword:
		if err := identity.CheckPasswordPolicy(ctx, state.Pool, inv.IdentityID, body.Password, state.PasswordPolicy(), inv.TenantID); err != nil {
			writeInvitationError(w, err)
			return
		}
	case inv.HasPassword:
		ident, err := identity.Authenticate(ctx, state.Pool, inv.Email, body.Password, now)
		if err != nil {
			writeIdentityError(w, err)
			return
		}
		if ident.ID != inv.IdentityID {
			writeInvitationError(w, identity.ErrInvitationInvalid)
			return
		}
	default:
		writeInvitationError(w, identity.ErrInvitationNeedsSignIn)
		return
	}

	tx, err := state.Pool.Begin(ctx)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.AcceptInvitation(ctx, tx, inv, now); err != nil {
		writeInvitationError(w, err)
		return
	}
	if setsPassword {
		if err := identity.SetPassword(ctx, tx, inv.IdentityID, body.Password, now); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	if name := strings.TrimSpace(body.DisplayName); name != "" {
		if err := identity.UpdateMember(ctx, tx, inv.TenantID, inv.MemberID, name); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalError(w, err)
		return
	}

	memberships, err := identity.Memberships(ctx, state.Pool, inv.IdentityID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	for _, m := range memberships {
		if m.MemberID == inv.MemberID {
			// SetPassword revoked earlier sessions; this one is minted after, so it survives.
			startSession(w, r, state, inv.IdentityID, m, time.Now())
			return
		}
	}
	// Joined, but the tenant itself is not active (suspended): nothing to sign in to yet.
	writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess,
		"Invitation accepted, but this tenant is not active right now.", nil)
}

type forgotPasswordBody struct {
	Email string `json:"email"`
}

// forgotPassword always answers 202 the same way and sends the email in the background, so neither
// the answer nor its timing tells whether an account exists. At most MaxPasswordResetsPerHour links
// per account and hour.
func forgotPassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body forgotPasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	email := strings.TrimSpace(body.Email)
	go sendPasswordReset(context.WithoutCancel(r.Context()), state, email)
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"accepted": true}})
}

func sendPasswordReset(ctx context.Context, state *AppState, email string) {
	now := time.Now()
	ident, err := identity.FindIdentityByEmail(ctx, state.Pool, email)
	if err != nil {
		if !errors.Is(err, identity.ErrNotFound) {
			slog.Error("password reset: looking up identity", standard.LogError, err)
		}
		return
	}
	if ident.Status != identity.StatusActive {
		return
	}
	token, err := identity.CreatePasswordReset(ctx, state.Pool, ident.ID, now)
	if err != nil {
		slog.Error("password reset: issuing link", standard.LogError, err)
		return
	}
	if token == "" {
		slog.Warn("password reset: hourly limit reached", "identity_id", ident.ID)
		return
	}
	if err := state.Mailer.SendPasswordReset(ctx, accountmail.PasswordReset{
		To: ident.Email, Link: state.Mailer.Link("/reset-password", token), ExpiresAt: now.Add(identity.PasswordResetTTL),
	}); err != nil {
		slog.Error("password reset: sending email", "identity_id", ident.ID, standard.LogError, err)
	}
}

type resetPasswordBody struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// resetPassword sets the new password from a reset link. It signs nobody in (every session of the
// account is revoked): the person signs in with the new password next.
func resetPassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body resetPasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	_, err := identity.ResetPasswordWithToken(ctx, state.Pool, strings.TrimSpace(body.Token), body.Password, time.Now(),
		func(identityID uuid.UUID) error {
			return identity.CheckPasswordPolicy(ctx, state.Pool, identityID, body.Password, state.PasswordPolicy())
		})
	if err != nil {
		writeInvitationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
