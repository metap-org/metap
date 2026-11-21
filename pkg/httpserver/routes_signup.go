package httpserver

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/captcha"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
)

const (
	signupTier       = "trial"
	maxTenantNameLen = 200
)

func signupEnabled(state *AppState) bool {
	return state.Config != nil && state.Config.Current().GetBool(config.SignupEnabled)
}

type signupBody struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	TenantName   string `json:"tenantName"`
	DisplayName  string `json:"displayName"`
	CaptchaToken string `json:"captchaToken"`
}

// reservedTenantNames can never be registered through signup: names that would let a stranger pass
// as the platform, its staff or a well-known brand. Compared by normalizeTenantName, so "Admin",
// "A.D.M.I.N" and "admin " are the same. An operator adds more with signup.blockedTenantNames.
var reservedTenantNames = []string{
	"admin", "administrator", "root", "system", "support", "security", "staff", "official", "billing",
	"help", "helpdesk", "abuse", "postmaster", "webmaster", "noreply", "metap", "platform", "default",
	"test", "null", "undefined", "api", "www", "mail", "status",
}

// normalizeTenantName folds a name to lower-case letters and digits, so punctuation, spacing and
// case cannot be used to slip past the blocklist.
func normalizeTenantName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tenantNameBlocked reports whether name is reserved or in the operator's list.
func tenantNameBlocked(name string, extra []string) bool {
	n := normalizeTenantName(name)
	if n == "" {
		return true
	}
	for _, list := range [][]string{reservedTenantNames, extra} {
		for _, b := range list {
			if n == normalizeTenantName(b) {
				return true
			}
		}
	}
	return false
}

// clientIP is who is calling: the TCP peer, or behind the configured trusted proxies the address
// they report (never a header from a peer that is not trusted).
func clientIP(r *http.Request, state *AppState) string {
	return httpx.ClientIP(r, state.TrustedProxies)
}

// signup starts self-service registration (roadmap 115 M2): it validates what the person typed,
// emails a link and creates nothing else. It answers 202 whether or not the address is already
// registered or was rate limited, so it cannot be used to find out who has an account.
func signup(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	if !signupEnabled(state) {
		writeServiceError(w, http.StatusForbidden, errcode.SignupDisabled, "Self-service signup is not open on this deployment.", nil)
		return
	}
	var body signupBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	fe := map[string][]string{}
	name := strings.TrimSpace(body.TenantName)
	if name == "" || len(name) > maxTenantNameLen {
		fe["tenantName"] = []string{"A name of 1 to 200 characters is required."}
	}
	if name != "" && len(name) <= maxTenantNameLen && state.tenantNameBlocked(name) {
		fe["tenantName"] = []string{"This name is not available."}
	}
	if _, err := identity.NormalizeEmail(body.Email); err != nil {
		fe["email"] = []string{err.Error()}
	}
	if err := identity.ValidatePasswordLength(body.Password, state.PasswordPolicy()(r.Context(), uuid.Nil)); err != nil {
		fe["password"] = []string{err.Error()}
	}
	if len(fe) > 0 {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "Invalid signup.", fe)
		return
	}
	if state.Captcha != nil {
		if err := state.Captcha.Verify(r.Context(), body.CaptchaToken, clientIP(r, state)); err != nil {
			if errors.Is(err, captcha.ErrFailed) {
				writeServiceError(w, http.StatusBadRequest, errcode.CaptchaFailed, "Please complete the verification and try again.",
					map[string][]string{"captchaToken": {"Verification failed."}})
				return
			}
			// The provider is down or unreachable: say so, don't blame the person.
			slog.Error("signup: captcha provider error", standard.LogError, err)
			writeServiceError(w, http.StatusServiceUnavailable, errcode.CaptchaFailed, "Verification is unavailable, try again shortly.", nil)
			return
		}
	}
	go sendSignupVerification(context.WithoutCancel(r.Context()), state, body)
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"accepted": true}})
}

func (s *AppState) tenantNameBlocked(name string) bool {
	var extra []string
	if s.Config != nil {
		extra = s.Config.Current().GetStrings(config.SignupBlockedTenantNames)
	}
	return tenantNameBlocked(name, extra)
}

func sendSignupVerification(ctx context.Context, state *AppState, body signupBody) {
	now := time.Now()
	_ = identity.PurgeSignups(ctx, state.Pool, now)
	token, err := identity.CreateSignup(ctx, state.Pool, body.Email, body.TenantName, body.DisplayName, body.Password, now)
	if err != nil {
		slog.Error("signup: recording request", standard.LogError, err)
		return
	}
	if token == "" {
		slog.Warn("signup: hourly limit reached for an address")
		return
	}
	link := state.Mailer.Link("/signup/verify", token)
	// Development aid: with LOG_LEVEL=debug the link (and so the token) is in the log, so signup can be
	// tried without a mail server. Never logged at info level or above.
	slog.Debug("signup verification link (debug log only)", "email", strings.TrimSpace(body.Email), "link", link, "token", token)
	if err := state.Mailer.SendSignupVerification(ctx, accountmail.SignupVerification{
		To: strings.TrimSpace(body.Email), TenantName: strings.TrimSpace(body.TenantName),
		Link: link, ExpiresAt: now.Add(identity.SignupTTL),
	}); err != nil && !errors.Is(err, accountmail.ErrNotConfigured) {
		slog.Error("signup: sending email", standard.LogError, err)
	}
}

type signupVerifyBody struct {
	Token string `json:"token"`
}

// signupVerify turns a confirmed signup into a tenant with the person as its admin, then signs
// them in. An address that already has an account keeps its password: the link proved they own the
// address, not that they may replace the password. If that account has a second factor, the usual
// second step follows.
func signupVerify(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	if !signupEnabled(state) {
		writeServiceError(w, http.StatusForbidden, errcode.SignupDisabled, "Self-service signup is not open on this deployment.", nil)
		return
	}
	var body signupVerifyBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	now := time.Now()
	tx, err := state.Pool.Begin(ctx)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	su, err := identity.ConsumeSignup(ctx, tx, body.Token, now)
	if err != nil {
		if errors.Is(err, identity.ErrSignupInvalid) {
			writeServiceError(w, http.StatusBadRequest, errcode.SignupInvalid, err.Error(), nil)
			return
		}
		writeInternalError(w, err)
		return
	}
	// The blocklist may have grown since the link was sent: check again before creating anything.
	// Returning here rolls the transaction back, so the (useless) link is not consumed.
	if state.tenantNameBlocked(su.TenantName) {
		writeServiceError(w, http.StatusBadRequest, errcode.SignupInvalid, "This name is not available.",
			map[string][]string{"tenantName": {"This name is not available."}})
		return
	}
	tenantID := uuid.New()
	registry := control.NewPostgresTenantRegistry(state.Pool)
	// The tenant is registered outside the signup transaction (the registry owns its writes), so a
	// failure after this point must undo it by hand.
	if err := registry.RegisterShared(ctx, tenantID, su.TenantName, signupTier, "provisioning"); err != nil {
		writeInternalError(w, err)
		return
	}
	fail := func(cause error) {
		_, _ = registry.Deprovision(context.WithoutCancel(ctx), tenantID)
		writeInternalError(w, cause)
	}
	m, _, err := identity.AddMember(ctx, state.Pool, identity.NewMember{
		TenantID: tenantID, Email: su.Email, DisplayName: su.DisplayName, PasswordHash: su.PasswordHash,
	})
	if err != nil {
		fail(err)
		return
	}
	if err := peripherals.EnsureBuiltinRoles(ctx, state.Pool, tenantID); err != nil {
		fail(err)
		return
	}
	if err := peripherals.AssignRole(ctx, state.Pool, tenantID, m.ID, permission.RoleAdmin, nil); err != nil {
		fail(err)
		return
	}
	if ok, err := registry.SetStatus(ctx, tenantID, "active"); err != nil || !ok {
		fail(errors.Join(err, errors.New("could not activate the tenant")))
		return
	}
	// Only now is the link used up: any failure above left it usable for a retry.
	if err := tx.Commit(ctx); err != nil {
		fail(err)
		return
	}
	ident, err := identity.FindIdentityByID(ctx, state.Pool, m.IdentityID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	ms := identity.Membership{MemberID: m.ID, TenantID: tenantID, TenantName: su.TenantName}
	if mfaGate(w, r, state, ident, ms, now) {
		return
	}
	startSession(w, r, state, ident.ID, ms, now)
}
