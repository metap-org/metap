package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// mfaGate sits between "the password was right" and "mint a session" (roadmap 115 M3). It answers
// the request itself and returns true when a second step is needed: a person with a second factor
// gets {mfaRequired, mfaToken} and posts the code to /auth/login/mfa; a person without one, in a
// tenant that requires MFA, gets {mfaSetupRequired, mfaToken} and enrols through /auth/mfa/setup.
func mfaGate(w http.ResponseWriter, r *http.Request, state *AppState, ident *identity.Identity, m identity.Membership, now time.Time) bool {
	enabled, err := identity.MFAEnabled(r.Context(), state.Pool, ident.ID)
	if err != nil {
		writeInternalError(w, err)
		return true
	}
	purpose, key := "", ""
	switch {
	case enabled:
		purpose, key = identity.ChallengeSignIn, "mfaRequired"
	case state.EffectiveConfig(r.Context(), m.TenantID).GetBool(config.AuthRequireMFA):
		purpose, key = identity.ChallengeSetup, "mfaSetupRequired"
	default:
		return false
	}
	_ = identity.PurgeChallenges(r.Context(), state.Pool, now)
	tenant := m.TenantID
	token, err := identity.CreateChallenge(r.Context(), state.Pool, ident.ID, &tenant, purpose, now)
	if err != nil {
		writeInternalError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{key: true, "mfaToken": token, "tenantId": m.TenantID}})
	return true
}

type mfaCodeBody struct {
	MFAToken string `json:"mfaToken"`
	Code     string `json:"code"`
}

func writeMFAError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrMFAChallenge):
		writeServiceError(w, http.StatusUnauthorized, errcode.MFAChallenge, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAInvalidCode):
		writeServiceError(w, http.StatusUnauthorized, errcode.MFAInvalidCode, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAAlreadyOn):
		writeServiceError(w, http.StatusConflict, errcode.MFAAlreadyOn, err.Error(), nil)
	case errors.Is(err, identity.ErrMFANotEnrolled), errors.Is(err, identity.ErrMFAEnrollNotRun):
		writeServiceError(w, http.StatusConflict, errcode.MFANotEnrolled, err.Error(), nil)
	default:
		writeInternalError(w, err)
	}
}

// membershipFor finds the identity's membership in the challenge's tenant, still active.
func membershipFor(r *http.Request, state *AppState, c *identity.Challenge) (identity.Membership, bool, error) {
	ms, err := identity.Memberships(r.Context(), state.Pool, c.IdentityID)
	if err != nil || c.TenantID == nil {
		return identity.Membership{}, false, err
	}
	m, ok := identity.PickMembership(ms, c.TenantID, nil)
	return m, ok, nil
}

// loginMFA is the second step of a sign-in: the challenge token plus a TOTP or recovery code.
func loginMFA(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	now := time.Now()
	c, err := identity.LookupChallenge(r.Context(), state.Pool, body.MFAToken, identity.ChallengeSignIn, now)
	if err != nil {
		writeMFAError(w, err)
		return
	}
	tx, err := state.Pool.Begin(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err := identity.VerifyMFA(r.Context(), tx, c.IdentityID, body.Code, now); err != nil {
		_ = tx.Rollback(r.Context())
		if errors.Is(err, identity.ErrMFAInvalidCode) {
			_ = identity.FailChallenge(r.Context(), state.Pool, body.MFAToken)
		}
		writeMFAError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	m, ok, err := membershipFor(r, state, c)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess, "This account has no access to that tenant.", nil)
		return
	}
	_ = identity.ConsumeChallenge(r.Context(), state.Pool, body.MFAToken)
	startSession(w, r, state, c.IdentityID, m, now)
}

// mfaSetup starts enrolment for a person the tenant forces to have a second factor.
func mfaSetup(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	now := time.Now()
	c, err := identity.LookupChallenge(r.Context(), state.Pool, body.MFAToken, identity.ChallengeSetup, now)
	if err != nil {
		writeMFAError(w, err)
		return
	}
	ident, err := identity.FindIdentityByID(r.Context(), state.Pool, c.IdentityID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	secret, uri, err := identity.BeginMFAEnrollment(r.Context(), state.Pool, c.IdentityID, ident.Email, now)
	if err != nil {
		writeMFAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"secret": secret, "otpauthUri": uri}})
}

// mfaSetupConfirm checks the first code, turns the factor on, shows the recovery codes once and
// finishes the sign-in that was waiting on it.
func mfaSetupConfirm(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	now := time.Now()
	c, err := identity.LookupChallenge(r.Context(), state.Pool, body.MFAToken, identity.ChallengeSetup, now)
	if err != nil {
		writeMFAError(w, err)
		return
	}
	tx, err := state.Pool.Begin(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	codes, err := identity.ConfirmMFAEnrollment(r.Context(), tx, c.IdentityID, body.Code, now)
	if err != nil {
		_ = tx.Rollback(r.Context())
		if errors.Is(err, identity.ErrMFAInvalidCode) {
			_ = identity.FailChallenge(r.Context(), state.Pool, body.MFAToken)
		}
		writeMFAError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	m, ok, err := membershipFor(r, state, c)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess, "This account has no access to that tenant.", nil)
		return
	}
	_ = identity.ConsumeChallenge(r.Context(), state.Pool, body.MFAToken)
	startSessionWith(w, r, state, c.IdentityID, m, now, map[string]any{"recoveryCodes": codes})
}
