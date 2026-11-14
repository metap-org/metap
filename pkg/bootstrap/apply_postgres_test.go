//go:build integration

package bootstrap

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/testkit"
)

func TestApplyUsersAgainstPostgres(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	ctx := context.Background()
	tenant := uuid.New()
	b := Bootstrap{
		Roles: []string{"analyst"},
		Users: []User{{Email: "Soc@Example.io", Roles: []string{"analyst"}, PasswordRef: "BOOTSTRAP_SOC_PW"}},
	}
	t.Setenv("BOOTSTRAP_SOC_PW", "correct horse battery staple")
	deps := Deps{Registry: registry(t), Policies: &fakePolicies{}, Identity: PgIdentity{DB: pool}}

	res, err := Apply(ctx, deps, tenant, b, Options{Production: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Soc@Example.io"}, res.UsersCreated)

	user, err := peripherals.VerifyCredentials(ctx, pool, "Soc@Example.io", "correct horse battery staple")
	require.NoError(t, err)
	require.NotNil(t, user)
	roles, err := peripherals.GetRolesForUser(ctx, pool, tenant, user.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"analyst"}, roles)

	// Re-run with the password gone: the account exists, so nothing is needed and nothing changes.
	t.Setenv("BOOTSTRAP_SOC_PW", "")
	res, err = Apply(ctx, deps, tenant, b, Options{Production: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Soc@Example.io"}, res.UsersExisting)
	assert.Empty(t, res.UsersCreated)
}
