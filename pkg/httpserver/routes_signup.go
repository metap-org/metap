package httpserver

import (
	"context"
	"net/http"

	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/signin"
)

type signupBody struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	TenantName   string `json:"tenantName"`
	DisplayName  string `json:"displayName"`
	CaptchaToken string `json:"captchaToken"`
}

// clientIP is who is calling: the TCP peer, or behind the configured trusted proxies the address
// they report (never a header from a peer that is not trusted).
func clientIP(r *http.Request, state *AppState) string {
	return httpx.ClientIP(r, state.TrustedProxies)
}

// signupStatus tells a sign-in page whether to offer "create an account". Deliberately its own
// route rather than a public config key: only tenant-tier keys may be served unauthenticated.
func signupStatus(w http.ResponseWriter, _ *http.Request, state *AppState) {
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"enabled": state.CanIssueTokens() && state.Signin().SignupEnabled()}})
}

// signup starts self-service registration (roadmap 115 M2): it validates what the person typed,
// emails a link and creates nothing else. It answers 202 whether or not the address is already
// registered or was rate limited, so it cannot be used to find out who has an account.
func signup(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body signupBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	svc := state.Signin()
	req := signin.SignupRequest{Email: body.Email, Password: body.Password, TenantName: body.TenantName,
		DisplayName: body.DisplayName, CaptchaToken: body.CaptchaToken}
	if err := svc.ValidateSignup(r.Context(), req, clientIP(r, state)); err != nil {
		writeCrudError(w, err)
		return
	}
	go svc.DeliverSignup(context.WithoutCancel(r.Context()), req)
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"accepted": true}})
}

type signupVerifyBody struct {
	Token string `json:"token"`
}

// signupVerify turns a confirmed signup into a tenant with the person as its admin, then signs
// them in (signin.Service.VerifySignup).
func signupVerify(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body signupVerifyBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	out, err := state.Signin().VerifySignup(r.Context(), body.Token, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeOutcome(w, state, out)
}
