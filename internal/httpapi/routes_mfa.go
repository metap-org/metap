package httpapi

import "net/http"

type mfaCodeBody struct {
	MFAToken string `json:"mfaToken"`
	Code     string `json:"code"`
}

// loginMFA is the second step of a sign-in: the challenge token plus a TOTP or recovery code.
func loginMFA(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, err := state.Signin().LoginMFA(r.Context(), body.MFAToken, body.Code, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}

// mfaSetup starts enrolment for a person the tenant forces to have a second factor.
func mfaSetup(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	secret, uri, err := state.Signin().MFASetupStart(r.Context(), body.MFAToken)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"secret": secret, "otpauthUri": uri}})
}

// mfaSetupConfirm checks the first code, turns the factor on, shows the recovery codes once and
// finishes the sign-in that was waiting on it.
func mfaSetupConfirm(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body mfaCodeBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, err := state.Signin().MFASetupConfirm(r.Context(), body.MFAToken, body.Code, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}
