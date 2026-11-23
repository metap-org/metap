//go:build integration

package signin_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/signin"
	"github.com/metap-org/metap/pkg/testkit"
)

func newService(t *testing.T) (*signin.Service, context.Context) {
	t.Helper()
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	store := config.Unloaded(pool)
	require.NoError(t, store.Reload(ctx))
	return signin.New(signin.Deps{
		Pool:      pool,
		Effective: func(context.Context, uuid.UUID) *config.EffectiveConfig { return store.Effective(nil) },
		MintSession: func(tenant, member uuid.UUID, ttl uint64, sessionID string) (string, error) {
			return "token-" + sessionID, nil
		},
		PasswordPolicy: func(context.Context, uuid.UUID) int { return identity.MinPasswordLength },
	}), ctx
}

func serviceErr(t *testing.T, err error) *crud.ServiceError {
	t.Helper()
	se, ok := crud.AsServiceError(err)
	require.True(t, ok, "expected a *crud.ServiceError, got %v", err)
	return se
}

func TestLoginIssuesASessionOrSaysWhatIsMissing(t *testing.T) {
	s, ctx := newService(t)
	email := uuid.NewString() + "@example.com"
	tenantA, tenantB := uuid.New(), uuid.New()
	mA, _, err := identity.AddMember(ctx, s.Pool(), identity.NewMember{TenantID: tenantA, Email: email, Password: "first-password"})
	require.NoError(t, err)
	_, _, err = identity.AddMember(ctx, s.Pool(), identity.NewMember{TenantID: tenantB, Email: email})
	require.NoError(t, err)
	client := signin.Client{IP: "203.0.113.9", UserAgent: "test"}

	// A wrong password is the sign-in error, not an internal one.
	_, err = s.Login(ctx, signin.LoginRequest{Email: email, Password: "nope"}, client)
	se := serviceErr(t, err)
	assert.Equal(t, uint16(http.StatusUnauthorized), se.Status)
	assert.Equal(t, errcode.InvalidCredentials, se.Code)

	// Two tenants and none named: the list comes back instead of a session.
	out, err := s.Login(ctx, signin.LoginRequest{Email: email, Password: "first-password"}, client)
	require.NoError(t, err)
	assert.Nil(t, out.Session)
	assert.Len(t, out.SelectTenant, 2)

	// Naming one gives a session whose token carries its row.
	out, err = s.Login(ctx, signin.LoginRequest{Email: email, Password: "first-password", TenantID: &tenantA}, client)
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	assert.Equal(t, tenantA, out.Session.TenantID)
	assert.Contains(t, out.Session.Token, "token-")
	assert.NotZero(t, out.Session.TTLSeconds)

	// A tenant the person does not belong to is refused.
	other := uuid.New()
	_, err = s.Login(ctx, signin.LoginRequest{Email: email, Password: "first-password", TenantID: &other}, client)
	assert.Equal(t, errcode.NoTenantAccess, serviceErr(t, err).Code)

	// Switching tenant needs the caller to be a person.
	rc := &permission.RequestContext{TenantID: tenantA.String(), UserID: mA.ID.String()}
	g, err := s.SwitchTenant(ctx, rc, tenantB, client)
	require.NoError(t, err)
	assert.Equal(t, tenantB, g.TenantID)
	_, err = s.SwitchTenant(ctx, &permission.RequestContext{TenantID: tenantA.String(), UserID: "not-a-uuid"}, tenantB, client)
	assert.Equal(t, errcode.Forbidden, serviceErr(t, err).Code)
}

func TestSecondFactorStepsAndPasswordChange(t *testing.T) {
	s, ctx := newService(t)
	email := uuid.NewString() + "@example.com"
	tenant := uuid.New()
	m, _, err := identity.AddMember(ctx, s.Pool(), identity.NewMember{TenantID: tenant, Email: email, Password: "first-password"})
	require.NoError(t, err)
	client := signin.Client{}

	secret, _, err := identity.BeginMFAEnrollment(ctx, s.Pool(), m.IdentityID, email, time.Now())
	require.NoError(t, err)
	code, err := identity.TOTPAt(secret, time.Now())
	require.NoError(t, err)
	_, err = identity.ConfirmMFAEnrollment(ctx, s.Pool(), m.IdentityID, code, time.Now())
	require.NoError(t, err)

	// The password alone is not a session any more: a challenge to answer instead.
	out, err := s.Login(ctx, signin.LoginRequest{Email: email, Password: "first-password"}, client)
	require.NoError(t, err)
	require.NotNil(t, out.MFARequired)
	assert.Nil(t, out.Session)

	// A wrong code is the second-factor error and burns the attempt.
	_, err = s.LoginMFA(ctx, out.MFARequired.Token, "000000", client)
	assert.Equal(t, errcode.MFAInvalidCode, serviceErr(t, err).Code)

	// Changing the password checks the current one and ends every other session.
	rc := &permission.RequestContext{TenantID: tenant.String(), UserID: m.ID.String()}
	_, err = s.ChangePassword(ctx, rc, "wrong", "a-new-password-1", client)
	assert.Equal(t, errcode.WrongPassword, serviceErr(t, err).Code)
	g, err := s.ChangePassword(ctx, rc, "first-password", "a-new-password-1", client)
	require.NoError(t, err)
	assert.Equal(t, tenant, g.TenantID)
}
