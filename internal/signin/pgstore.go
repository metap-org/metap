package signin

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// PgStore is the Postgres adapter: everything on the shared identity database except a member's address
// (the tenant's own, through the router) and the OIDC client secret (the secret store).
type PgStore struct {
	pool   *pgxpool.Pool
	router *tenancy.Router
	policy identity.PasswordPolicy
}

func NewPgStore(pool *pgxpool.Pool, router *tenancy.Router, policy identity.PasswordPolicy) *PgStore {
	return &PgStore{pool: pool, router: router, policy: policy}
}

func (s *PgStore) Authenticate(ctx context.Context, email, password string, now time.Time) (*identity.Identity, error) {
	return identity.Authenticate(ctx, s.pool, email, password, now)
}
func (s *PgStore) FindIdentityByID(ctx context.Context, id uuid.UUID) (*identity.Identity, error) {
	return identity.FindIdentityByID(ctx, s.pool, id)
}
func (s *PgStore) FindIdentityByEmail(ctx context.Context, email string) (*identity.Identity, error) {
	return identity.FindIdentityByEmail(ctx, s.pool, email)
}
func (s *PgStore) Memberships(ctx context.Context, person uuid.UUID) ([]identity.Membership, error) {
	return identity.Memberships(ctx, s.pool, person)
}
func (s *PgStore) IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error) {
	return identity.IdentityOf(ctx, s.pool, tenant, member)
}
func (s *PgStore) OwnerID(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	return identity.OwnerID(ctx, s.pool, tenant)
}

func (s *PgStore) UserEmail(ctx context.Context, tenant, member uuid.UUID) string {
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return ""
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	user, err := identity.FindUserByID(ctx, tx, tenant, member)
	if err != nil || user == nil {
		return ""
	}
	return user.Email
}

func (s *PgStore) VerifyCurrentPassword(ctx context.Context, person uuid.UUID, pw string, now time.Time) error {
	return identity.VerifyCurrentPassword(ctx, s.pool, person, pw, now)
}
func (s *PgStore) CheckPassword(ctx context.Context, person uuid.UUID, pw string, tenant uuid.UUID) error {
	if tenant == uuid.Nil {
		return identity.CheckPasswordPolicy(ctx, s.pool, person, pw, s.policy)
	}
	return identity.CheckPasswordPolicy(ctx, s.pool, person, pw, s.policy, tenant)
}
func (s *PgStore) RequiredPasswordLength(ctx context.Context, person, tenant uuid.UUID) (int, error) {
	return identity.RequiredPasswordLength(ctx, s.pool, person, s.policy, tenant)
}
func (s *PgStore) SetPassword(ctx context.Context, person uuid.UUID, pw string, now time.Time) error {
	return identity.SetPassword(ctx, s.pool, person, pw, now)
}

func (s *PgStore) PurgeSessions(ctx context.Context, now time.Time) error {
	return identity.PurgeSessions(ctx, s.pool, now)
}
func (s *PgStore) CreateSession(ctx context.Context, person, member, tenant uuid.UUID, ttl time.Duration, ip, ua string, now time.Time) (uuid.UUID, error) {
	return identity.CreateSession(ctx, s.pool, person, member, tenant, ttl, ip, ua, now)
}
func (s *PgStore) RevokeSession(ctx context.Context, person, session uuid.UUID, now time.Time) error {
	return identity.RevokeSession(ctx, s.pool, person, session, now)
}
func (s *PgStore) ExtendSession(ctx context.Context, session uuid.UUID, ttl time.Duration, now time.Time) error {
	return identity.ExtendSession(ctx, s.pool, session, ttl, now)
}
func (s *PgStore) RecordSignIn(ctx context.Context, person, member, tenant uuid.UUID, now time.Time) error {
	return identity.RecordSignIn(ctx, s.pool, person, member, tenant, now)
}

func (s *PgStore) MFAEnabled(ctx context.Context, person uuid.UUID) (bool, error) {
	return identity.MFAEnabled(ctx, s.pool, person)
}
func (s *PgStore) PurgeChallenges(ctx context.Context, now time.Time) error {
	return identity.PurgeChallenges(ctx, s.pool, now)
}
func (s *PgStore) CreateChallenge(ctx context.Context, person uuid.UUID, tenant *uuid.UUID, purpose string, now time.Time) (string, error) {
	return identity.CreateChallenge(ctx, s.pool, person, tenant, purpose, now)
}
func (s *PgStore) LookupChallenge(ctx context.Context, token, purpose string, now time.Time) (*identity.Challenge, error) {
	return identity.LookupChallenge(ctx, s.pool, token, purpose, now)
}
func (s *PgStore) FailChallenge(ctx context.Context, token string) error {
	return identity.FailChallenge(ctx, s.pool, token)
}
func (s *PgStore) ConsumeChallenge(ctx context.Context, token string) error {
	return identity.ConsumeChallenge(ctx, s.pool, token)
}

// VerifyMFA runs in its own transaction so a wrong code is counted (committed) although the call fails,
// while any other failure rolls back.
func (s *PgStore) VerifyMFA(ctx context.Context, person uuid.UUID, code string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.VerifyMFA(ctx, tx, person, code, now); err != nil {
		if identity.MFAAttemptCounts(err) {
			_ = tx.Commit(ctx) // keep the wrong-code count across sign-in attempts
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) BeginMFAEnrollment(ctx context.Context, person uuid.UUID, account string, now time.Time) (string, string, error) {
	return identity.BeginMFAEnrollment(ctx, s.pool, person, account, now)
}

func (s *PgStore) ConfirmMFAEnrollment(ctx context.Context, person uuid.UUID, code string, now time.Time) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	codes, err := identity.ConfirmMFAEnrollment(ctx, tx, person, code, now)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit(ctx)
}

func (s *PgStore) FindInvitation(ctx context.Context, token string, now time.Time) (*identity.Invitation, error) {
	return identity.FindInvitation(ctx, s.pool, token, now)
}
func (s *PgStore) CanSetPasswordOnAccept(ctx context.Context, person, member uuid.UUID) (bool, error) {
	return identity.CanSetPasswordOnAccept(ctx, s.pool, person, member)
}

func (s *PgStore) AcceptInvitation(ctx context.Context, inv *identity.Invitation, setPassword bool, password, displayName string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.AcceptInvitation(ctx, tx, inv, now); err != nil {
		return err
	}
	if setPassword {
		if err := identity.SetPassword(ctx, tx, inv.IdentityID, password, now); err != nil {
			return err
		}
	}
	if displayName != "" {
		if err := identity.UpdateMember(ctx, tx, inv.TenantID, inv.MemberID, displayName); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PgStore) CreatePasswordReset(ctx context.Context, person uuid.UUID, now time.Time) (string, error) {
	return identity.CreatePasswordReset(ctx, s.pool, person, now)
}
func (s *PgStore) ResetPassword(ctx context.Context, token, password string, now time.Time, validate func(uuid.UUID) error) error {
	_, err := identity.ResetPasswordWithToken(ctx, s.pool, token, password, now, validate)
	return err
}

func (s *PgStore) ConsumeDeletionCancelToken(ctx context.Context, token string, now time.Time) (uuid.UUID, error) {
	return identity.ConsumeDeletionCancelToken(ctx, s.pool, token, now)
}
func (s *PgStore) CancelDeletion(ctx context.Context, tenant uuid.UUID, now time.Time) error {
	return tenancy.NewPostgresTenantRegistry(s.pool).CancelDeletion(ctx, tenant, now)
}

func (s *PgStore) PurgeSignups(ctx context.Context, now time.Time) error {
	return identity.PurgeSignups(ctx, s.pool, now)
}
func (s *PgStore) CreateSignup(ctx context.Context, email, tenantName, displayName, password string, now time.Time) (string, error) {
	return identity.CreateSignup(ctx, s.pool, email, tenantName, displayName, password, now)
}
func (s *PgStore) ConsumeSignup(ctx context.Context, token string, now time.Time) (*identity.Signup, error) {
	return identity.ConsumeSignup(ctx, s.pool, token, now)
}
func (s *PgStore) RestoreSignup(ctx context.Context, su *identity.Signup) error {
	return identity.RestoreSignup(ctx, s.pool, su)
}

// CreateWorkspace registers the tenant, adds the person as a member with the admin role and activates
// it. The registry owns its writes, so a failure after the tenant exists undoes it by hand.
func (s *PgStore) CreateWorkspace(ctx context.Context, tenant uuid.UUID, su *identity.Signup, tier string) (*identity.Member, error) {
	registry := tenancy.NewPostgresTenantRegistry(s.pool)
	if err := registry.RegisterShared(ctx, tenant, su.TenantName, tier, "provisioning"); err != nil {
		return nil, err
	}
	fail := func(cause error) (*identity.Member, error) {
		_, _ = registry.Deprovision(context.WithoutCancel(ctx), tenant)
		return nil, cause
	}
	m, _, err := identity.AddMember(ctx, s.pool, identity.NewMember{
		TenantID: tenant, Email: su.Email, DisplayName: su.DisplayName, PasswordHash: su.PasswordHash,
	})
	if err != nil {
		return fail(err)
	}
	if err := rolestore.EnsureBuiltinRoles(ctx, s.pool, tenant); err != nil {
		return fail(err)
	}
	if err := rolestore.AssignRole(ctx, s.pool, tenant, m.ID, permission.RoleAdmin, nil); err != nil {
		return fail(err)
	}
	if ok, err := registry.SetStatus(ctx, tenant, "active"); err != nil || !ok {
		return fail(errors.Join(err, errors.New("could not activate the tenant")))
	}
	return m, nil
}

func (s *PgStore) SSOEnforced(ctx context.Context, tenant uuid.UUID) (bool, error) {
	return sso.SSOEnforced(ctx, s.pool, tenant)
}
func (s *PgStore) TenantsForEmailDomain(ctx context.Context, domain string) ([]uuid.UUID, error) {
	return sso.TenantsForEmailDomain(ctx, s.pool, domain)
}
func (s *PgStore) ProviderOf(ctx context.Context, tenant uuid.UUID) (string, error) {
	return sso.ProviderOf(ctx, s.pool, tenant)
}
func (s *PgStore) OidcConfigFor(ctx context.Context, tenant uuid.UUID) (*sso.OidcConfig, error) {
	return sso.OidcConfigFor(ctx, s.pool, tenant)
}
func (s *PgStore) SamlConfigFor(ctx context.Context, tenant uuid.UUID) (*sso.SamlConfig, error) {
	return sso.SamlConfigFor(ctx, s.pool, tenant)
}
func (s *PgStore) ClientSecret(ctx context.Context, ref string) (string, error) {
	return s.router.Secrets().GetSecret(ctx, ref)
}
func (s *PgStore) CreateSSOState(ctx context.Context, tenant uuid.UUID, state, nonce, verifier string, now time.Time) error {
	return sso.CreateSSOState(ctx, s.pool, tenant, state, nonce, verifier, now)
}
func (s *PgStore) ConsumeSSOState(ctx context.Context, state string, now time.Time) (*sso.SSOState, error) {
	return sso.ConsumeSSOState(ctx, s.pool, state, now)
}
func (s *PgStore) DomainVerifiedFor(ctx context.Context, tenant uuid.UUID, address string) (bool, error) {
	return sso.DomainVerifiedFor(ctx, s.pool, tenant, address)
}

func (s *PgStore) FindExternalMember(ctx context.Context, tenant uuid.UUID, provider, subject string) (*identity.Member, error) {
	return identity.FindExternalMember(ctx, s.pool, tenant, provider, subject)
}
func (s *PgStore) MemberByEmail(ctx context.Context, tenant uuid.UUID, email string) (*identity.Member, error) {
	return identity.MemberByEmail(ctx, s.pool, tenant, email)
}
func (s *PgStore) LinkExternalSubject(ctx context.Context, tenant, member uuid.UUID, provider, subject string) error {
	return identity.LinkExternalSubject(ctx, s.pool, tenant, member, provider, subject)
}
func (s *PgStore) AddExternalMember(ctx context.Context, tenant uuid.UUID, email, provider, subject, defaultRole string) (*identity.Member, error) {
	m, _, err := identity.AddMember(ctx, s.pool, identity.NewMember{TenantID: tenant, Email: email, AuthProvider: provider, ExternalSubject: subject})
	if err != nil {
		return nil, err
	}
	if defaultRole != "" {
		if err := rolestore.AssignRole(ctx, s.pool, tenant, m.ID, defaultRole, nil); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func (s *PgStore) RolesOf(ctx context.Context, tenant, member uuid.UUID) ([]string, error) {
	return rolestore.GetRolesForUser(ctx, s.pool, tenant, member)
}
func (s *PgStore) AssignRole(ctx context.Context, tenant, member uuid.UUID, role string) error {
	return rolestore.AssignRole(ctx, s.pool, tenant, member, role, nil)
}
func (s *PgStore) RevokeRole(ctx context.Context, tenant, member uuid.UUID, role string) error {
	return rolestore.RevokeRole(ctx, s.pool, tenant, member, role)
}
