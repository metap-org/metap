package signin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/captcha"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/standard"
)

const (
	signupTier       = "trial"
	maxTenantNameLen = 200
)

// reservedTenantNames can never be registered through signup: names that would let a stranger pass
// as the platform, its staff or a well-known brand. Compared by normalizeTenantName, so "Admin",
// "A.D.M.I.N" and "admin " are the same. An operator adds more with signup.blockedTenantNames.
var reservedTenantNames = []string{
	"admin", "administrator", "root", "system", "support", "security", "staff", "official", "billing",
	"help", "helpdesk", "abuse", "postmaster", "webmaster", "noreply", "metap", "platform", "default",
	"test", "null", "undefined", "api", "www", "mail", "status",
}

func (s *Service) platform() *config.Snapshot {
	if s.d.Platform == nil {
		return nil
	}
	return s.d.Platform()
}

// SignupEnabled reports whether self-service registration is open on this deployment.
func (s *Service) SignupEnabled() bool {
	p := s.platform()
	return p != nil && p.GetBool(config.SignupEnabled)
}

func (s *Service) tenantNameBlocked(name string) bool {
	var extra []string
	if p := s.platform(); p != nil {
		extra = p.GetStrings(config.SignupBlockedTenantNames)
	}
	return TenantNameBlocked(name, extra)
}

// SignupRequest is what a person typed to register a workspace.
type SignupRequest struct {
	Email        string
	Password     string
	TenantName   string
	DisplayName  string
	CaptchaToken string
}

// ValidateSignup checks a signup (the fields, then the captcha). Nothing is created: DeliverSignup
// does that part, in the background, so the answer is the same whatever the address is.
func (s *Service) ValidateSignup(ctx context.Context, req SignupRequest, clientIP string) error {
	if !s.SignupEnabled() {
		return errSignupDisabled()
	}
	fe := map[string][]string{}
	name := strings.TrimSpace(req.TenantName)
	if name == "" || len(name) > maxTenantNameLen {
		fe["tenantName"] = []string{"A name of 1 to 200 characters is required."}
	}
	if name != "" && len(name) <= maxTenantNameLen && s.tenantNameBlocked(name) {
		fe["tenantName"] = []string{"This name is not available."}
	}
	if _, err := identity.NormalizeEmail(req.Email); err != nil {
		fe["email"] = []string{err.Error()}
	}
	if err := identity.ValidatePasswordLength(req.Password, s.d.PasswordPolicy(ctx, uuid.Nil)); err != nil {
		fe["password"] = []string{err.Error()}
	}
	if len(fe) > 0 {
		return serviceErr(http.StatusBadRequest, errcode.ValidationFailed, "Invalid signup.", fe)
	}
	if s.d.Captcha != nil {
		if err := s.d.Captcha.Verify(ctx, req.CaptchaToken, clientIP); err != nil {
			if errors.Is(err, captcha.ErrFailed) {
				return serviceErr(http.StatusBadRequest, errcode.CaptchaFailed, "Please complete the verification and try again.",
					map[string][]string{"captchaToken": {"Verification failed."}})
			}
			// The provider is down or unreachable: say so, don't blame the person.
			slog.Error("signup: captcha provider error", standard.LogError, err)
			return serviceErr(http.StatusServiceUnavailable, errcode.CaptchaFailed, "Verification is unavailable, try again shortly.", nil)
		}
	}
	return nil
}

// DeliverSignup records the request and emails the verification link. It reports nothing (run it in
// the background): an address that already has an account, or one that was rate limited, must look
// like any other.
func (s *Service) DeliverSignup(ctx context.Context, req SignupRequest) {
	now := s.now()
	_ = s.store.PurgeSignups(ctx, now)
	token, err := s.store.CreateSignup(ctx, req.Email, req.TenantName, req.DisplayName, req.Password, now)
	if err != nil {
		slog.Error("signup: recording request", standard.LogError, err)
		return
	}
	if token == "" {
		slog.Warn("signup: hourly limit reached for an address")
		return
	}
	link := s.d.Mailer.Link("/signup/verify", token)
	// Development aid: with LOG_LEVEL=debug the link (and so the token) is in the log, so signup can be
	// tried without a mail server. Never logged at info level or above.
	slog.Debug("signup verification link (debug log only)", "email", strings.TrimSpace(req.Email), "link", link, "token", token)
	if err := s.d.Mailer.SendSignupVerification(ctx, mail.SignupVerification{
		To: strings.TrimSpace(req.Email), TenantName: strings.TrimSpace(req.TenantName),
		Link: link, ExpiresAt: now.Add(identity.SignupTTL),
	}); err != nil && !errors.Is(err, mail.ErrNotConfigured) {
		slog.Error("signup: sending email", standard.LogError, err)
	}
}

// VerifySignup turns a confirmed signup into a tenant with the person as its admin, then signs them
// in. An address that already has an account keeps its password: the link proved they own the
// address, not that they may replace the password. If that account has a second factor, the usual
// second step follows.
func (s *Service) VerifySignup(ctx context.Context, token string, c Client) (*Outcome, error) {
	if !s.SignupEnabled() {
		return nil, errSignupDisabled()
	}
	// The link is claimed with one statement and no connection is held while the tenant is built:
	// each step below takes its own pooled connection, so holding one open around them would need
	// two per request. A failure puts the signup back so the link stays usable.
	su, err := s.store.ConsumeSignup(ctx, token, s.now())
	if err != nil {
		if errors.Is(err, identity.ErrSignupInvalid) {
			return nil, serviceErr(http.StatusBadRequest, errcode.SignupInvalid, err.Error(), nil)
		}
		return nil, err
	}
	restore := func() { _ = s.store.RestoreSignup(context.WithoutCancel(ctx), su) }
	// The blocklist may have grown since the link was sent: check again before creating anything.
	// The signup goes back, so the link is not consumed.
	if s.tenantNameBlocked(su.TenantName) {
		restore()
		return nil, serviceErr(http.StatusBadRequest, errcode.SignupInvalid, "This name is not available.",
			map[string][]string{"tenantName": {"This name is not available."}})
	}
	tenantID := uuid.New()
	// The workspace is made whole or not at all; a failure puts the signup back so the link stays usable.
	m, err := s.store.CreateWorkspace(ctx, tenantID, su, signupTier)
	if err != nil {
		restore()
		return nil, err
	}
	// Only now is the link used up: any failure above put it back for a retry.
	ident, err := s.store.FindIdentityByID(ctx, m.IdentityID)
	if err != nil {
		return nil, err
	}
	return s.LoginAfterPassword(ctx, ident, identity.Membership{MemberID: m.ID, TenantID: tenantID, TenantName: su.TenantName}, c)
}
