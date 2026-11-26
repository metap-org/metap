package signin

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

// fakeStore is an in-memory Store. It embeds the interface, so a method a test does not exercise panics
// instead of silently answering.
type fakeStore struct {
	Store

	person      *identity.Identity
	password    string
	memberships []identity.Membership
	members     map[uuid.UUID]*identity.Member
	owner       uuid.UUID
	enforced    bool
	mfa         bool
	mfaCode     string
	challenges  map[string]*identity.Challenge

	sessions   []createdSession
	revoked    []uuid.UUID
	extended   []uuid.UUID
	signedIn   int
	failed     []string
	consumed   []string
	passwords  []string
	roles      map[uuid.UUID][]string
	linked     []string
	added      []string
	domainOK   bool
	external   *identity.Member
	invitation *identity.Invitation
	setsPass   bool
}

type createdSession struct {
	member, tenant uuid.UUID
	ip, ua         string
}

func (f *fakeStore) Authenticate(_ context.Context, email, password string, _ time.Time) (*identity.Identity, error) {
	if f.person == nil || email != f.person.Email || password != f.password {
		return nil, identity.ErrInvalidCredentials
	}
	return f.person, nil
}
func (f *fakeStore) Memberships(context.Context, uuid.UUID) ([]identity.Membership, error) {
	return f.memberships, nil
}
func (f *fakeStore) IdentityOf(_ context.Context, _, member uuid.UUID) (uuid.UUID, error) {
	if f.person == nil || !slices.ContainsFunc(f.memberships, func(m identity.Membership) bool { return m.MemberID == member }) {
		return uuid.Nil, identity.ErrNotFound
	}
	return f.person.ID, nil
}
func (f *fakeStore) OwnerID(context.Context, uuid.UUID) (uuid.UUID, error) { return f.owner, nil }
func (f *fakeStore) SSOEnforced(context.Context, uuid.UUID) (bool, error)  { return f.enforced, nil }
func (f *fakeStore) MFAEnabled(context.Context, uuid.UUID) (bool, error)   { return f.mfa, nil }
func (f *fakeStore) PurgeSessions(context.Context, time.Time) error        { return nil }
func (f *fakeStore) PurgeChallenges(context.Context, time.Time) error      { return nil }
func (f *fakeStore) CreateSession(_ context.Context, _, member, tenant uuid.UUID, _ time.Duration, ip, ua string, _ time.Time) (uuid.UUID, error) {
	f.sessions = append(f.sessions, createdSession{member, tenant, ip, ua})
	return uuid.New(), nil
}
func (f *fakeStore) ExtendSession(_ context.Context, session uuid.UUID, _ time.Duration, _ time.Time) error {
	f.extended = append(f.extended, session)
	return nil
}
func (f *fakeStore) RecordSignIn(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	f.signedIn++
	return nil
}
func (f *fakeStore) RevokeSession(_ context.Context, _, session uuid.UUID, _ time.Time) error {
	f.revoked = append(f.revoked, session)
	return nil
}
func (f *fakeStore) CreateChallenge(_ context.Context, person uuid.UUID, tenant *uuid.UUID, purpose string, _ time.Time) (string, error) {
	token := "challenge-" + purpose
	f.challenges[token] = &identity.Challenge{IdentityID: person, TenantID: tenant, Purpose: purpose}
	return token, nil
}
func (f *fakeStore) LookupChallenge(_ context.Context, token, purpose string, _ time.Time) (*identity.Challenge, error) {
	if c, ok := f.challenges[token]; ok && c.Purpose == purpose {
		return c, nil
	}
	return nil, identity.ErrMFAChallenge
}
func (f *fakeStore) VerifyMFA(_ context.Context, _ uuid.UUID, code string, _ time.Time) error {
	if code != f.mfaCode {
		return identity.ErrMFAInvalidCode
	}
	return nil
}
func (f *fakeStore) FailChallenge(_ context.Context, token string) error {
	f.failed = append(f.failed, token)
	return nil
}
func (f *fakeStore) ConsumeChallenge(_ context.Context, token string) error {
	f.consumed = append(f.consumed, token)
	delete(f.challenges, token)
	return nil
}
func (f *fakeStore) VerifyCurrentPassword(_ context.Context, _ uuid.UUID, pw string, _ time.Time) error {
	if pw != f.password {
		return identity.ErrInvalidCredentials
	}
	return nil
}
func (f *fakeStore) CheckPassword(_ context.Context, _ uuid.UUID, pw string, _ uuid.UUID) error {
	if len(pw) < 8 {
		return identity.ErrWeakPassword
	}
	return nil
}
func (f *fakeStore) SetPassword(_ context.Context, _ uuid.UUID, pw string, _ time.Time) error {
	f.passwords = append(f.passwords, pw)
	return nil
}
func (f *fakeStore) FindInvitation(context.Context, string, time.Time) (*identity.Invitation, error) {
	if f.invitation == nil {
		return nil, identity.ErrInvitationInvalid
	}
	return f.invitation, nil
}
func (f *fakeStore) CanSetPasswordOnAccept(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.setsPass, nil
}
func (f *fakeStore) FindExternalMember(context.Context, uuid.UUID, string, string) (*identity.Member, error) {
	return f.external, nil
}
func (f *fakeStore) DomainVerifiedFor(context.Context, uuid.UUID, string) (bool, error) {
	return f.domainOK, nil
}
func (f *fakeStore) MemberByEmail(_ context.Context, _ uuid.UUID, email string) (*identity.Member, error) {
	for _, m := range f.members {
		if m.Email == email {
			return m, nil
		}
	}
	return nil, identity.ErrNotFound
}
func (f *fakeStore) LinkExternalSubject(_ context.Context, _, member uuid.UUID, _, subject string) error {
	f.linked = append(f.linked, member.String()+"="+subject)
	return nil
}
func (f *fakeStore) AddExternalMember(_ context.Context, tenant uuid.UUID, email, _, subject, role string) (*identity.Member, error) {
	m := &identity.Member{ID: uuid.New(), IdentityID: uuid.New(), TenantID: tenant, Email: email, Status: identity.StatusActive}
	f.members[m.ID] = m
	f.added = append(f.added, email+"/"+role)
	return m, nil
}
func (f *fakeStore) RolesOf(_ context.Context, _, member uuid.UUID) ([]string, error) {
	return f.roles[member], nil
}
func (f *fakeStore) AssignRole(_ context.Context, _, member uuid.UUID, role string) error {
	f.roles[member] = append(f.roles[member], role)
	return nil
}
func (f *fakeStore) RevokeRole(_ context.Context, _, member uuid.UUID, role string) error {
	f.roles[member] = slices.DeleteFunc(f.roles[member], func(r string) bool { return r == role })
	return nil
}

type world struct {
	svc       *Service
	store     *fakeStore
	tenantA   uuid.UUID
	tenantB   uuid.UUID
	memberA   uuid.UUID
	memberB   uuid.UUID
	now       time.Time
	notIssuer bool
	minted    []string
}

func newWorld() *world {
	w := &world{tenantA: uuid.New(), tenantB: uuid.New(), memberA: uuid.New(), memberB: uuid.New(), now: time.Now()}
	w.store = &fakeStore{
		person:   &identity.Identity{ID: uuid.New(), Email: "me@acme.io", Status: identity.StatusActive},
		password: "right-password",
		memberships: []identity.Membership{
			{MemberID: w.memberA, TenantID: w.tenantA, TenantName: "Acme"},
		},
		members: map[uuid.UUID]*identity.Member{}, roles: map[uuid.UUID][]string{}, challenges: map[string]*identity.Challenge{},
	}
	cfg := config.Unloaded(nil)
	w.svc = New(Deps{
		Store:     w.store,
		Effective: func(context.Context, uuid.UUID) *config.EffectiveConfig { return cfg.Effective(nil) },
		MintSession: func(tenant, member uuid.UUID, ttl uint64, sid string) (string, error) {
			if w.notIssuer {
				return "", ErrNotTokenIssuer
			}
			tok := "tok-" + member.String()[:4] + "-" + sid[:4]
			w.minted = append(w.minted, tok)
			return tok, nil
		},
		Now: func() time.Time { return w.now },
	})
	return w
}

func (w *world) second() {
	w.store.memberships = append(w.store.memberships, identity.Membership{MemberID: w.memberB, TenantID: w.tenantB, TenantName: "Globex"})
}

func (w *world) caller() *permission.RequestContext {
	return &permission.RequestContext{TenantID: w.tenantA.String(), UserID: w.memberA.String(), SessionID: uuid.NewString()}
}

var browser = Client{IP: "203.0.113.7", UserAgent: "test-agent"}

func statusOf(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func TestLoginSignsInAndRecordsWhereFrom(t *testing.T) {
	w := newWorld()
	out, err := w.svc.Login(context.Background(), LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	assert.Equal(t, w.tenantA, out.Session.TenantID)
	assert.NotEmpty(t, out.Session.Token)
	require.Len(t, w.store.sessions, 1)
	assert.Equal(t, createdSession{w.memberA, w.tenantA, "203.0.113.7", "test-agent"}, w.store.sessions[0])
	assert.Equal(t, 1, w.store.signedIn)
}

func TestLoginRefusals(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	_, err := w.svc.Login(ctx, LoginRequest{Email: "me@acme.io", Password: "wrong"}, browser)
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))
	assert.Empty(t, w.store.sessions)

	w.store.memberships = nil
	_, err = w.svc.Login(ctx, LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no tenant at all")

	w = newWorld()
	other := uuid.New()
	_, err = w.svc.Login(ctx, LoginRequest{Email: "me@acme.io", Password: "right-password", TenantID: &other}, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "a tenant they are not in")

	w = newWorld()
	w.notIssuer = true
	_, err = w.svc.Login(ctx, LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	assert.Equal(t, http.StatusNotImplemented, statusOf(err), "a verifier-only service issues no token")
}

func TestSeveralTenantsAskWhichOne(t *testing.T) {
	w := newWorld()
	w.second()
	out, err := w.svc.Login(context.Background(), LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	require.NoError(t, err)
	assert.Nil(t, out.Session)
	assert.Len(t, out.SelectTenant, 2)
	assert.Empty(t, w.store.sessions, "no session until they choose")

	out, err = w.svc.Login(context.Background(), LoginRequest{Email: "me@acme.io", Password: "right-password", TenantID: &w.tenantB}, browser)
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	assert.Equal(t, w.tenantB, out.Session.TenantID)
}

func TestEnforcedSSORefusesPasswordsExceptTheOwners(t *testing.T) {
	w := newWorld()
	w.store.enforced = true
	_, err := w.svc.Login(context.Background(), LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Empty(t, w.store.sessions)

	w.store.owner = w.memberA
	out, err := w.svc.Login(context.Background(), LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	require.NoError(t, err)
	assert.NotNil(t, out.Session, "the owner keeps the way back in")
}

func TestASecondFactorHoldsBackTheSession(t *testing.T) {
	w := newWorld()
	w.store.mfa, w.store.mfaCode = true, "123456"
	ctx := context.Background()
	out, err := w.svc.Login(ctx, LoginRequest{Email: "me@acme.io", Password: "right-password"}, browser)
	require.NoError(t, err)
	require.NotNil(t, out.MFARequired)
	assert.Nil(t, out.Session)
	assert.Empty(t, w.store.sessions)

	token := out.MFARequired.Token
	_, err = w.svc.LoginMFA(ctx, token, "000000", browser)
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))
	assert.Equal(t, []string{token}, w.store.failed, "a wrong code is counted against the challenge")
	assert.Empty(t, w.store.sessions)

	g, err := w.svc.LoginMFA(ctx, token, "123456", browser)
	require.NoError(t, err)
	assert.Equal(t, w.tenantA, g.TenantID)
	assert.Equal(t, []string{token}, w.store.consumed, "a challenge is used once")
	_, err = w.svc.LoginMFA(ctx, token, "123456", browser)
	assert.Error(t, err, "and not again")
}

func TestSwitchTenant(t *testing.T) {
	w := newWorld()
	w.second()
	w.store.person.ID = uuid.New()
	ctx := context.Background()
	g, err := w.svc.SwitchTenant(ctx, w.caller(), w.tenantB, browser)
	require.NoError(t, err)
	assert.Equal(t, w.tenantB, g.TenantID)

	_, err = w.svc.SwitchTenant(ctx, w.caller(), uuid.New(), browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	notPerson := &permission.RequestContext{TenantID: w.tenantA.String(), UserID: "service-account"}
	_, err = w.svc.SwitchTenant(ctx, notPerson, w.tenantB, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "a token that is not a person")

	w.store.enforced = true
	_, err = w.svc.SwitchTenant(ctx, w.caller(), w.tenantB, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "enforced SSO applies to a switch too")
}

func TestChangePasswordNeedsTheCurrentOneAndGivesAFreshSession(t *testing.T) {
	w := newWorld()
	c := context.Background()
	_, err := w.svc.ChangePassword(c, w.caller(), "wrong", "new-long-password", browser)
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.ChangePassword(c, w.caller(), "right-password", "short", browser)
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the policy applies")
	assert.Empty(t, w.store.passwords)

	g, err := w.svc.ChangePassword(c, w.caller(), "right-password", "new-long-password", browser)
	require.NoError(t, err)
	assert.Equal(t, []string{"new-long-password"}, w.store.passwords)
	assert.NotEmpty(t, g.Token, "the changing session survives: it is minted after")
	assert.Len(t, w.store.sessions, 1)
}

func TestEndSessionRevokesOnlyARealSession(t *testing.T) {
	w := newWorld()
	c := context.Background()
	w.svc.EndSession(c, nil)
	w.svc.EndSession(c, &permission.RequestContext{TenantID: w.tenantA.String(), UserID: w.memberA.String()})
	assert.Empty(t, w.store.revoked, "a token with no session has nothing to end")
	rc := w.caller()
	w.svc.EndSession(c, rc)
	assert.Len(t, w.store.revoked, 1)
}

func TestInvitationModes(t *testing.T) {
	w := newWorld()
	c := context.Background()
	_, err := w.svc.LookupInvitation(c, "nope")
	assert.Equal(t, http.StatusBadRequest, statusOf(err))

	w.store.invitation = &identity.Invitation{MemberID: w.memberA, IdentityID: w.store.person.ID, TenantID: w.tenantA, Email: "me@acme.io"}
	w.store.setsPass = false
	_, err = w.svc.AcceptInvitation(c, "t", "x", "", browser)
	assert.Equal(t, http.StatusConflict, statusOf(err), "an account with no password and no provider has to sign in first")

	w.store.setsPass = true
	_, err = w.svc.AcceptInvitation(c, "t", "short", "", browser)
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a new account's password meets the policy")
}

func TestFinishSSORules(t *testing.T) {
	w := newWorld()
	c := context.Background()
	policy := SSOPolicy{Provider: "oidc", DefaultRole: "viewer", Allowed: func(a string) bool { return a != "blocked@evil.io" }}
	who := &sso.VerifiedIdentity{Email: "new@acme.io", EmailVerified: true, ExternalSubject: "sub-1"}

	_, err := w.svc.FinishSSO(c, w.tenantA, policy, &sso.VerifiedIdentity{Email: "blocked@evil.io", EmailVerified: true, ExternalSubject: "s"}, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "outside the allowed domains")

	_, err = w.svc.FinishSSO(c, w.tenantA, policy, &sso.VerifiedIdentity{Email: "new@acme.io", ExternalSubject: "sub-1"}, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "the provider did not vouch for the address")

	_, err = w.svc.FinishSSO(c, w.tenantA, policy, who, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "the workspace has not verified the domain")
	assert.Empty(t, w.store.added)

	w.store.domainOK = true
	g, err := w.svc.FinishSSO(c, w.tenantA, policy, who, browser)
	require.NoError(t, err)
	assert.NotEmpty(t, g.Token)
	assert.Equal(t, []string{"new@acme.io/viewer"}, w.store.added, "provisioned with the default role")
}

func TestFinishSSOLinksAnExistingMemberAndSyncsMappedRoles(t *testing.T) {
	w := newWorld()
	c := context.Background()
	existing := &identity.Member{ID: uuid.New(), IdentityID: uuid.New(), Email: "ann@acme.io", Status: identity.StatusActive}
	w.store.members[existing.ID] = existing
	w.store.domainOK = true
	w.store.roles[existing.ID] = []string{"analyst", "custom"}
	policy := SSOPolicy{Provider: "oidc", Allowed: func(string) bool { return true },
		Mapping: sso.RoleMapping{Claim: "groups", Map: map[string]string{"soc": "analyst", "ops": "operator"}}}
	who := &sso.VerifiedIdentity{Email: "ann@acme.io", EmailVerified: true, ExternalSubject: "sub-ann", Roles: []string{"ops"}}

	_, err := w.svc.FinishSSO(c, w.tenantA, policy, who, browser)
	require.NoError(t, err)
	assert.Equal(t, []string{existing.ID.String() + "=sub-ann"}, w.store.linked)
	assert.ElementsMatch(t, []string{"custom", "operator"}, w.store.roles[existing.ID],
		"the claim decides the mapped roles only: analyst goes, operator comes, custom stays")

	// The owner keeps their roles whatever the provider says.
	w.store.owner = existing.ID
	w.store.roles[existing.ID] = []string{"analyst"}
	w.store.external = existing
	_, err = w.svc.FinishSSO(c, w.tenantA, policy, &sso.VerifiedIdentity{Email: "ann@acme.io", EmailVerified: true, ExternalSubject: "sub-ann"}, browser)
	require.NoError(t, err)
	assert.Contains(t, w.store.roles[existing.ID], "analyst")

	existing.Status = identity.StatusDisabled
	_, err = w.svc.FinishSSO(c, w.tenantA, policy, who, browser)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "a disabled member does not get in")
}

func TestRenewHonoursTheAbsoluteCap(t *testing.T) {
	w := newWorld()
	c := context.Background()
	rc := w.caller()
	r, ok := w.svc.Renew(c, rc, w.now.Add(-time.Minute))
	require.True(t, ok)
	assert.NotEmpty(t, r.Token)
	assert.Len(t, w.store.extended, 1, "the session row moves forward with the token")
	_, ok = w.svc.Renew(c, rc, w.now.Add(-365*24*time.Hour))
	assert.False(t, ok, "past the absolute cap")
	_, ok = w.svc.Renew(c, &permission.RequestContext{TenantID: "x", UserID: "y"}, w.now)
	assert.False(t, ok, "not ids")
}
