package signin

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/sso"
)

// Store is everything the sign-in service reads and writes: people and their passwords, sessions,
// second-factor challenges, invitations, resets and signups, and a tenant's single sign-on settings.
// It is one interface so the service can be tried with an in-memory fake (a test that needs only some
// of it embeds the interface and implements the rest as it needs); PgStore is the Postgres adapter and
// each method is the identity, sso, rolestore or tenancy call of the same name. Errors are theirs, which
// the service maps onto the envelope (rules.go).
type Store interface {
	People
	Sessions
	Challenges
	Joining
	Federation
}

// People is who can sign in and how they prove it.
type People interface {
	Authenticate(ctx context.Context, email, password string, now time.Time) (*identity.Identity, error)
	FindIdentityByID(ctx context.Context, id uuid.UUID) (*identity.Identity, error)
	FindIdentityByEmail(ctx context.Context, email string) (*identity.Identity, error)
	Memberships(ctx context.Context, person uuid.UUID) ([]identity.Membership, error)
	// IdentityOf is the person behind a member (identity.ErrNotFound: none).
	IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error)
	OwnerID(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error)
	// UserEmail is a member's address through the tenant's own database ("" when there is none).
	UserEmail(ctx context.Context, tenant, member uuid.UUID) string
	// VerifyCurrentPassword is a re-authentication: a wrong password counts toward the lockout.
	VerifyCurrentPassword(ctx context.Context, person uuid.UUID, password string, now time.Time) error
	// CheckPassword is nil when password passes the deployment's policy (tenant, when not uuid.Nil,
	// adds that tenant's own).
	CheckPassword(ctx context.Context, person uuid.UUID, password string, tenant uuid.UUID) error
	RequiredPasswordLength(ctx context.Context, person uuid.UUID, tenant uuid.UUID) (int, error)
	SetPassword(ctx context.Context, person uuid.UUID, password string, now time.Time) error
}

// Sessions are the rows every sign-in path ends in.
type Sessions interface {
	PurgeSessions(ctx context.Context, now time.Time) error
	CreateSession(ctx context.Context, person, member, tenant uuid.UUID, ttl time.Duration, ip, userAgent string, now time.Time) (uuid.UUID, error)
	RevokeSession(ctx context.Context, person, session uuid.UUID, now time.Time) error
	ExtendSession(ctx context.Context, session uuid.UUID, ttl time.Duration, now time.Time) error
	RecordSignIn(ctx context.Context, person, member, tenant uuid.UUID, now time.Time) error
}

// Challenges are the second step of a sign-in (roadmap 115 M3).
type Challenges interface {
	MFAEnabled(ctx context.Context, person uuid.UUID) (bool, error)
	PurgeChallenges(ctx context.Context, now time.Time) error
	CreateChallenge(ctx context.Context, person uuid.UUID, tenant *uuid.UUID, purpose string, now time.Time) (string, error)
	LookupChallenge(ctx context.Context, token, purpose string, now time.Time) (*identity.Challenge, error)
	FailChallenge(ctx context.Context, token string) error
	ConsumeChallenge(ctx context.Context, token string) error
	// VerifyMFA checks a code; a wrong one is counted across attempts even though the call fails.
	VerifyMFA(ctx context.Context, person uuid.UUID, code string, now time.Time) error
	BeginMFAEnrollment(ctx context.Context, person uuid.UUID, account string, now time.Time) (secret, uri string, err error)
	// ConfirmMFAEnrollment turns the factor on and returns the recovery codes, all or nothing.
	ConfirmMFAEnrollment(ctx context.Context, person uuid.UUID, code string, now time.Time) ([]string, error)
}

// Joining is how a person gets an account or a way back into one: invitations, password resets,
// self-service signup and a workspace's deletion being called off.
type Joining interface {
	FindInvitation(ctx context.Context, token string, now time.Time) (*identity.Invitation, error)
	CanSetPasswordOnAccept(ctx context.Context, person, member uuid.UUID) (bool, error)
	// AcceptInvitation activates the member, sets the password (when setPassword) and the display name
	// (when not empty): all or none.
	AcceptInvitation(ctx context.Context, inv *identity.Invitation, setPassword bool, password, displayName string, now time.Time) error

	CreatePasswordReset(ctx context.Context, person uuid.UUID, now time.Time) (string, error)
	// ResetPassword sets the password from a reset token, after validate approves it.
	ResetPassword(ctx context.Context, token, password string, now time.Time, validate func(person uuid.UUID) error) error

	ConsumeDeletionCancelToken(ctx context.Context, token string, now time.Time) (uuid.UUID, error)
	CancelDeletion(ctx context.Context, tenant uuid.UUID, now time.Time) error

	PurgeSignups(ctx context.Context, now time.Time) error
	CreateSignup(ctx context.Context, email, tenantName, displayName, password string, now time.Time) (string, error)
	ConsumeSignup(ctx context.Context, token string, now time.Time) (*identity.Signup, error)
	RestoreSignup(ctx context.Context, su *identity.Signup) error
	// CreateWorkspace makes the tenant (an active one) with the person as its admin, from a signup. A
	// failure leaves nothing behind.
	CreateWorkspace(ctx context.Context, tenant uuid.UUID, su *identity.Signup, tier string) (*identity.Member, error)
}

// Federation is a tenant's single sign-on: its settings, the state a sign-in carries, and the members
// an identity provider's subject maps to.
type Federation interface {
	SSOEnforced(ctx context.Context, tenant uuid.UUID) (bool, error)
	TenantsForEmailDomain(ctx context.Context, domain string) ([]uuid.UUID, error)
	ProviderOf(ctx context.Context, tenant uuid.UUID) (string, error)
	OidcConfigFor(ctx context.Context, tenant uuid.UUID) (*sso.OidcConfig, error)
	SamlConfigFor(ctx context.Context, tenant uuid.UUID) (*sso.SamlConfig, error)
	// ClientSecret is the OIDC client secret a configuration's reference points at.
	ClientSecret(ctx context.Context, ref string) (string, error)
	CreateSSOState(ctx context.Context, tenant uuid.UUID, state, nonce, verifier string, now time.Time) error
	ConsumeSSOState(ctx context.Context, state string, now time.Time) (*sso.SSOState, error)
	DomainVerifiedFor(ctx context.Context, tenant uuid.UUID, address string) (bool, error)

	FindExternalMember(ctx context.Context, tenant uuid.UUID, provider, subject string) (*identity.Member, error)
	MemberByEmail(ctx context.Context, tenant uuid.UUID, email string) (*identity.Member, error)
	LinkExternalSubject(ctx context.Context, tenant, member uuid.UUID, provider, subject string) error
	// AddExternalMember provisions a member for a provider's subject, with defaultRole when not empty.
	AddExternalMember(ctx context.Context, tenant uuid.UUID, email, provider, subject, defaultRole string) (*identity.Member, error)
	RolesOf(ctx context.Context, tenant, member uuid.UUID) ([]string, error)
	AssignRole(ctx context.Context, tenant, member uuid.UUID, role string) error
	RevokeRole(ctx context.Context, tenant, member uuid.UUID, role string) error
}
