// Package signin is how a person gets a session: the password sign-in and its second factor, the
// tenant switch, a password change that hands out a fresh session, and the session row and token
// every sign-in path (password, invitation, signup, SSO) ends in. It is plain Go (roadmap 118): no
// net/http. A failure is a *crud.ServiceError; what the HTTP layer keeps is cookies, redirects and
// the response body, and it takes what to put in them from a Grant.
package signin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/auth/captcha"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// ErrNotTokenIssuer: this service verifies tokens but holds no signing key.
var ErrNotTokenIssuer = errors.New("this service does not issue tokens; log in at the token issuer")

// Deps is what the service needs from the process that hosts it.
type Deps struct {
	Pool *pgxpool.Pool
	// Effective is the cache-first read of a tenant's effective configuration.
	Effective func(ctx context.Context, tenant uuid.UUID) *config.EffectiveConfig
	// MintSession signs a session token (ErrNotTokenIssuer when this service cannot).
	MintSession    func(tenant, member uuid.UUID, ttlSeconds uint64, sessionID string) (string, error)
	PasswordPolicy identity.PasswordPolicy
	// Platform is the platform-tier configuration (the signup switch and name blocklist); nil when
	// there is none.
	Platform func() *config.Snapshot
	// PublicURL is the origin people reach this service at (the SSO callback is PublicURL + /auth/sso/callback);
	// "" turns SSO off.
	PublicURL string
	// Router opens tenant-scoped transactions and resolves a tenant's stored credentials (the OIDC client secret).
	Router *tenancy.Router
	// Mailer sends the account emails; its methods are safe on a nil Mailer (they send none).
	Mailer *mail.Mailer
	// Captcha, when set, must pass for a signup.
	Captcha captcha.Verifier
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d: d} }

func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}

// Client is what the service records about where a sign-in came from.
type Client struct {
	IP        string
	UserAgent string
}

// Grant is a session that was just created: the token, and what the HTTP layer needs to set the
// cookies that carry it.
type Grant struct {
	Token    string
	TenantID uuid.UUID
	// TTLSeconds is the session token's lifetime.
	TTLSeconds uint64
	// AbsoluteMaxSeconds caps how long the session may be renewed after StartedAt.
	AbsoluteMaxSeconds uint64
	StartedAt          time.Time
	// RecoveryCodes are shown once, when a required-MFA enrolment finishes the sign-in.
	RecoveryCodes []string
}

// Issue creates the session row for membership m, mints its token and records the sign-in. It is
// what every sign-in path ends in; a fresh session's absolute-lifetime clock starts now.
func (s *Service) Issue(ctx context.Context, identityID uuid.UUID, m identity.Membership, c Client) (*Grant, error) {
	now := s.now()
	_ = identity.PurgeSessions(ctx, s.d.Pool, now)
	cfg := s.d.Effective(ctx, m.TenantID)
	ttl := cfg.GetU64(config.AuthSessionTTLSeconds)
	sessionID, err := identity.CreateSession(ctx, s.d.Pool, identityID, m.MemberID, m.TenantID,
		time.Duration(ttl)*time.Second, c.IP, c.UserAgent, now)
	if err != nil {
		return nil, err
	}
	token, err := s.d.MintSession(m.TenantID, m.MemberID, ttl, sessionID.String())
	if err != nil {
		if errors.Is(err, ErrNotTokenIssuer) {
			return nil, serviceErr(http.StatusNotImplemented, errcode.NotTokenIssuer, err.Error(), nil)
		}
		return nil, err
	}
	if err := identity.RecordSignIn(ctx, s.d.Pool, identityID, m.MemberID, m.TenantID, now); err != nil {
		return nil, err
	}
	return &Grant{Token: token, TenantID: m.TenantID, TTLSeconds: ttl,
		AbsoluteMaxSeconds: cfg.GetU64(config.AuthSessionAbsoluteMaxSeconds), StartedAt: now}, nil
}

// Challenge is the second step a sign-in still needs: the token to post back with a code.
type Challenge struct {
	Token    string
	TenantID uuid.UUID
}

// Outcome is where a password sign-in stands. Exactly one field is set.
type Outcome struct {
	Session *Grant
	// SelectTenant: the person belongs to several tenants and named none; post again with one.
	SelectTenant []identity.Membership
	// MFARequired: post the challenge token and a code to LoginMFA.
	MFARequired *Challenge
	// MFASetupRequired: the tenant demands a second factor the person has not set up.
	MFASetupRequired *Challenge
}

type LoginRequest struct {
	Email    string
	Password string
	TenantID *uuid.UUID
}

// Login signs a person in (roadmap 113). The password is checked against the identity; the session
// then lands in one tenant: the one asked for, else the person's only or last used tenant.
func (s *Service) Login(ctx context.Context, req LoginRequest, c Client) (*Outcome, error) {
	now := s.now()
	ident, err := identity.Authenticate(ctx, s.d.Pool, req.Email, req.Password, now)
	if err != nil {
		return nil, IdentityError(err)
	}
	memberships, err := identity.Memberships(ctx, s.d.Pool, ident.ID)
	if err != nil {
		return nil, err
	}
	m, ok := identity.PickMembership(memberships, req.TenantID, ident.LastTenantID)
	if !ok {
		switch {
		case len(memberships) == 0:
			return nil, serviceErr(http.StatusForbidden, errcode.NoTenantAccess, "This account has no access to any active tenant.", nil)
		case req.TenantID != nil:
			return nil, serviceErr(http.StatusForbidden, errcode.NoTenantAccess, "This account has no access to that tenant.", nil)
		}
		return &Outcome{SelectTenant: memberships}, nil
	}
	return s.proceed(ctx, ident, m, c, now)
}

// proceed is what follows "the password was right": SSO enforcement, the second factor, the session.
func (s *Service) proceed(ctx context.Context, ident *identity.Identity, m identity.Membership, c Client, now time.Time) (*Outcome, error) {
	if err := s.RequirePasswordAllowed(ctx, m); err != nil {
		return nil, err
	}
	if ch, setup, err := s.mfaGate(ctx, ident, m, now); err != nil {
		return nil, err
	} else if ch != nil {
		if setup {
			return &Outcome{MFASetupRequired: ch}, nil
		}
		return &Outcome{MFARequired: ch}, nil
	}
	g, err := s.Issue(ctx, ident.ID, m, c)
	if err != nil {
		return nil, err
	}
	return &Outcome{Session: g}, nil
}

// LoginAfterPassword continues a sign-in whose password was already proven (signup verification):
// the same SSO, second-factor and session steps Login runs after it.
func (s *Service) LoginAfterPassword(ctx context.Context, ident *identity.Identity, m identity.Membership, c Client) (*Outcome, error) {
	return s.proceed(ctx, ident, m, c, s.now())
}

// RequirePasswordAllowed refuses a password sign-in (or tenant switch) into a tenant that enforces
// SSO, unless the member is the tenant's owner: the way back in when the identity provider is broken.
func (s *Service) RequirePasswordAllowed(ctx context.Context, m identity.Membership) error {
	enforced, err := sso.SSOEnforced(ctx, s.d.Pool, m.TenantID)
	if err != nil {
		return err
	}
	if !enforced {
		return nil
	}
	owner, err := identity.OwnerID(ctx, s.d.Pool, m.TenantID)
	if err != nil {
		return err
	}
	if owner == m.MemberID {
		return nil
	}
	return serviceErr(http.StatusForbidden, errcode.SSORequired, "This workspace signs in through its identity provider.", nil)
}

// SwitchTenant moves the caller's session to another tenant they are a member of, without asking
// for the password again.
func (s *Service) SwitchTenant(ctx context.Context, caller *permission.RequestContext, tenant uuid.UUID, c Client) (*Grant, error) {
	identityID, err := s.callerIdentity(ctx, caller)
	if err != nil {
		return nil, err
	}
	memberships, err := identity.Memberships(ctx, s.d.Pool, identityID)
	if err != nil {
		return nil, err
	}
	m, found := identity.PickMembership(memberships, &tenant, nil)
	if !found {
		return nil, serviceErr(http.StatusForbidden, errcode.NoTenantAccess, "You are not a member of that tenant.", nil)
	}
	if err := s.RequirePasswordAllowed(ctx, m); err != nil {
		return nil, err
	}
	return s.Issue(ctx, identityID, m, c)
}

// callerIdentity is the identity behind the caller's token; a token whose subject is not a person
// (a service principal) is refused.
func (s *Service) callerIdentity(ctx context.Context, rc *permission.RequestContext) (uuid.UUID, error) {
	notPerson := serviceErr(http.StatusForbidden, errcode.Forbidden, "This token does not belong to a person.", nil)
	tenantID, err1 := uuid.Parse(rc.TenantID)
	userID, err2 := uuid.Parse(rc.UserID)
	if err1 != nil || err2 != nil {
		return uuid.Nil, notPerson
	}
	id, err := identity.IdentityOf(ctx, s.d.Pool, tenantID, userID)
	if errors.Is(err, identity.ErrNotFound) {
		return uuid.Nil, notPerson
	}
	return id, err
}

// ChangePassword sets the caller's own password. It revokes every session of the person (all
// tenants, all devices) and hands this one a fresh session.
func (s *Service) ChangePassword(ctx context.Context, caller *permission.RequestContext, current, next string, c Client) (*Grant, error) {
	identityID, err := s.callerIdentity(ctx, caller)
	if err != nil {
		return nil, err
	}
	// A wrong current password counts toward the same lockout as a sign-in: a stolen session must
	// not be a way to guess the password.
	switch err := identity.VerifyCurrentPassword(ctx, s.d.Pool, identityID, current, s.now()); {
	case errors.Is(err, identity.ErrInvalidCredentials):
		return nil, serviceErr(http.StatusBadRequest, errcode.WrongPassword, "The current password is wrong.", nil)
	case err != nil:
		return nil, IdentityError(err)
	}
	if err := identity.CheckPasswordPolicy(ctx, s.d.Pool, identityID, next, s.d.PasswordPolicy); err != nil {
		return nil, IdentityError(err)
	}
	if err := identity.SetPassword(ctx, s.d.Pool, identityID, next, s.now()); err != nil {
		return nil, IdentityError(err)
	}
	tenantID, _ := uuid.Parse(caller.TenantID)
	userID, _ := uuid.Parse(caller.UserID)
	return s.Issue(ctx, identityID, identity.Membership{MemberID: userID, TenantID: tenantID}, c)
}

// EndSession revokes the caller's session on the server, so a copied token stops working. Best
// effort: with no session row there is nothing to end.
func (s *Service) EndSession(ctx context.Context, caller *permission.RequestContext) {
	if caller == nil || caller.SessionID == "" {
		return
	}
	sid, err := uuid.Parse(caller.SessionID)
	if err != nil {
		return
	}
	tenant, _ := uuid.Parse(caller.TenantID)
	user, _ := uuid.Parse(caller.UserID)
	if ident, err := identity.IdentityOf(ctx, s.d.Pool, tenant, user); err == nil {
		_ = identity.RevokeSession(ctx, s.d.Pool, ident, sid, s.now())
	}
}

func serviceErr(status uint16, code, message string, fieldErrors map[string][]string) error {
	return &crud.ServiceError{Status: status, Code: code, Message: message, FieldErrors: fieldErrors}
}

// IdentityError maps pkg/identity's sign-in and password errors onto the platform error envelope;
// anything else is returned as it is (an internal error).
func IdentityError(err error) error {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		return serviceErr(http.StatusUnauthorized, errcode.InvalidCredentials, "Invalid email or password.", nil)
	case errors.Is(err, identity.ErrLocked):
		return serviceErr(http.StatusTooManyRequests, errcode.AccountLocked, err.Error(), nil)
	case errors.Is(err, identity.ErrDisabled):
		return serviceErr(http.StatusForbidden, errcode.AccountDisabled, err.Error(), nil)
	case errors.Is(err, identity.ErrWeakPassword):
		return serviceErr(http.StatusBadRequest, errcode.WeakPassword, err.Error(), map[string][]string{"password": {err.Error()}})
	}
	return err
}

// Pool is the database the service reads and writes, for tests and tools that set people up.
func (s *Service) Pool() *pgxpool.Pool { return s.d.Pool }

func isNotIssuer(err error) bool { return errors.Is(err, ErrNotTokenIssuer) }
