// Public account self-service (roadmap 113 P2): accepting an invitation and resetting a forgotten
// password. REST, like login, because accepting signs the person in (sets the session cookie) and
// none of it has a session to authenticate a typed call with. Tokens travel in POST bodies, never
// in a URL this server logs. The flows themselves are pkg/signin's.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/metap-org/metap/pkg/signin"
)

type tokenBody struct {
	Token string `json:"token"`
}

// writeInvitationError maps pkg/identity's invitation and reset errors onto the error envelope.
func writeInvitationError(w http.ResponseWriter, err error) {
	writeCrudError(w, signin.InvitationError(err))
}

// lookupInvitation tells the accept page what to ask for: a new password (a brand-new account) or
// the current one (an existing account), and the policy it must meet.
func lookupInvitation(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body tokenBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	v, err := state.Signin().LookupInvitation(r.Context(), body.Token)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"email": v.Email, "displayName": v.DisplayName, "tenantId": v.TenantID,
		"tenantName": nullableString(v.TenantName), "expiresAt": v.ExpiresAt.UTC().Format(time.RFC3339),
		// "set": choose a password; "confirm": type the current one; "provider": sign in elsewhere.
		"passwordMode": v.PasswordMode, "minPasswordLength": v.MinPasswordLength,
	}})
}

type acceptInvitationBody struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// acceptInvitation joins the tenant and signs the person in (signin.Service.AcceptInvitation).
func acceptInvitation(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body acceptInvitationBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, err := state.Signin().AcceptInvitation(r.Context(), body.Token, body.Password, body.DisplayName, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}

type forgotPasswordBody struct {
	Email string `json:"email"`
}

// forgotPassword always answers 202 the same way and sends the email in the background, so neither
// the answer nor its timing tells whether an account exists.
func forgotPassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body forgotPasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	go state.Signin().RequestPasswordReset(context.WithoutCancel(r.Context()), strings.TrimSpace(body.Email))
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"accepted": true}})
}

type resetPasswordBody struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// resetPassword sets the new password from a reset link. It signs nobody in (every session of the
// account is revoked): the person signs in with the new password next.
func resetPassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body resetPasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := state.Signin().ResetPassword(r.Context(), body.Token, body.Password); err != nil {
		writeCrudError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type cancelDeletionBody struct {
	Token string `json:"token"`
}

// cancelWorkspaceDeletion is POST /auth/workspace-deletion/cancel: the emailed link's token reopens
// a workspace whose owner asked to delete it (UC-81). Public, like the other link-token routes,
// because the workspace is suspended and nobody can sign in to it meanwhile.
func cancelWorkspaceDeletion(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body cancelDeletionBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := state.Signin().CancelWorkspaceDeletion(r.Context(), body.Token); err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"cancelled": true}})
}
