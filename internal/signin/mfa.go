package signin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// mfaGate sits between "the password was right" and "mint a session" (roadmap 115 M3): a person
// with a second factor gets a challenge to answer, a person without one in a tenant that requires
// MFA gets one to enrol through. setup says which. Both nil means no second step is needed.
func (s *Service) mfaGate(ctx context.Context, ident *identity.Identity, m identity.Membership, now time.Time) (ch *Challenge, setup bool, err error) {
	enabled, err := identity.MFAEnabled(ctx, s.d.Pool, ident.ID)
	if err != nil {
		return nil, false, err
	}
	purpose := ""
	switch {
	case enabled:
		purpose = identity.ChallengeSignIn
	case s.d.Effective(ctx, m.TenantID).GetBool(config.AuthRequireMFA):
		purpose, setup = identity.ChallengeSetup, true
	default:
		return nil, false, nil
	}
	_ = identity.PurgeChallenges(ctx, s.d.Pool, now)
	tenant := m.TenantID
	token, err := identity.CreateChallenge(ctx, s.d.Pool, ident.ID, &tenant, purpose, now)
	if err != nil {
		return nil, false, err
	}
	return &Challenge{Token: token, TenantID: m.TenantID}, setup, nil
}

// MFAError maps pkg/identity's second-factor errors onto the platform error envelope.
func MFAError(err error) error {
	switch {
	case errors.Is(err, identity.ErrMFAChallenge):
		return serviceErr(http.StatusUnauthorized, errcode.MFAChallenge, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAInvalidCode):
		return serviceErr(http.StatusUnauthorized, errcode.MFAInvalidCode, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAAlreadyOn):
		return serviceErr(http.StatusConflict, errcode.MFAAlreadyOn, err.Error(), nil)
	case errors.Is(err, identity.ErrMFANotEnrolled), errors.Is(err, identity.ErrMFAEnrollNotRun):
		return serviceErr(http.StatusConflict, errcode.MFANotEnrolled, err.Error(), nil)
	}
	return err
}

// membershipFor finds the identity's membership in the challenge's tenant, still active.
func (s *Service) membershipFor(ctx context.Context, c *identity.Challenge) (identity.Membership, error) {
	noAccess := serviceErr(http.StatusForbidden, errcode.NoTenantAccess, "This account has no access to that tenant.", nil)
	ms, err := identity.Memberships(ctx, s.d.Pool, c.IdentityID)
	if err != nil {
		return identity.Membership{}, err
	}
	if c.TenantID == nil {
		return identity.Membership{}, noAccess
	}
	m, ok := identity.PickMembership(ms, c.TenantID, nil)
	if !ok {
		return identity.Membership{}, noAccess
	}
	return m, nil
}

// LoginMFA is the second step of a sign-in: the challenge token plus a TOTP or recovery code.
func (s *Service) LoginMFA(ctx context.Context, token, code string, c Client) (*Grant, error) {
	now := s.now()
	ch, err := identity.LookupChallenge(ctx, s.d.Pool, token, identity.ChallengeSignIn, now)
	if err != nil {
		return nil, MFAError(err)
	}
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.VerifyMFA(ctx, tx, ch.IdentityID, code, now); err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, identity.ErrMFAInvalidCode) {
			_ = identity.FailChallenge(ctx, s.d.Pool, token)
		}
		return nil, MFAError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	m, err := s.membershipFor(ctx, ch)
	if err != nil {
		return nil, err
	}
	_ = identity.ConsumeChallenge(ctx, s.d.Pool, token)
	return s.Issue(ctx, ch.IdentityID, m, c)
}

// MFASetupStart begins enrolment for a person the tenant forces to have a second factor.
func (s *Service) MFASetupStart(ctx context.Context, token string) (secret, otpauthURI string, err error) {
	now := s.now()
	ch, err := identity.LookupChallenge(ctx, s.d.Pool, token, identity.ChallengeSetup, now)
	if err != nil {
		return "", "", MFAError(err)
	}
	ident, err := identity.FindIdentityByID(ctx, s.d.Pool, ch.IdentityID)
	if err != nil {
		return "", "", err
	}
	secret, otpauthURI, err = identity.BeginMFAEnrollment(ctx, s.d.Pool, ch.IdentityID, ident.Email, now)
	if err != nil {
		return "", "", MFAError(err)
	}
	return secret, otpauthURI, nil
}

// MFASetupConfirm checks the first code, turns the factor on and finishes the sign-in that was
// waiting on it; the recovery codes come back once, in the Grant.
func (s *Service) MFASetupConfirm(ctx context.Context, token, code string, c Client) (*Grant, error) {
	now := s.now()
	ch, err := identity.LookupChallenge(ctx, s.d.Pool, token, identity.ChallengeSetup, now)
	if err != nil {
		return nil, MFAError(err)
	}
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	codes, err := identity.ConfirmMFAEnrollment(ctx, tx, ch.IdentityID, code, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, identity.ErrMFAInvalidCode) {
			_ = identity.FailChallenge(ctx, s.d.Pool, token)
		}
		return nil, MFAError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	m, err := s.membershipFor(ctx, ch)
	if err != nil {
		return nil, err
	}
	_ = identity.ConsumeChallenge(ctx, s.d.Pool, token)
	g, err := s.Issue(ctx, ch.IdentityID, m, c)
	if err != nil {
		return nil, err
	}
	g.RecoveryCodes = codes
	return g, nil
}
