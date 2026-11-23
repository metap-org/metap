//go:build integration

package bootstrap

import (
	"context"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/secrets"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/testkit"
)

func TestApplyUsersAgainstPostgres(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	ctx := context.Background()
	tenant := uuid.New()
	b := Bootstrap{
		Roles: []string{"analyst"},
		Users: []User{{Email: "Soc@Example.io", Roles: []string{"analyst"}, SecretRef: "BOOTSTRAP_SOC_PW"}},
	}
	t.Setenv("BOOTSTRAP_SOC_PW", "correct horse battery staple")
	deps := Deps{Registry: registry(t), Policies: &fakePolicies{}, Identity: PgIdentity{DB: pool}}

	res, err := Apply(ctx, deps, tenant, b, Options{Production: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Soc@Example.io"}, res.UsersCreated)

	user, err := identity.VerifyCredentials(ctx, pool, "Soc@Example.io", "correct horse battery staple", nil)
	require.NoError(t, err)
	require.NotNil(t, user)
	roles, err := rolestore.GetRolesForUser(ctx, pool, tenant, user.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"analyst"}, roles)

	// Re-run with the password gone: the account exists, so nothing is needed and nothing changes.
	t.Setenv("BOOTSTRAP_SOC_PW", "")
	res, err = Apply(ctx, deps, tenant, b, Options{Production: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Soc@Example.io"}, res.UsersExisting)
	assert.Empty(t, res.UsersCreated)
}

func TestApplyPoliciesAgainstPostgres(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	ctx := context.Background()
	tenant := uuid.New()
	router := control.NewRouter(pool, control.NewRegistryCache(control.NewPostgresTenantRegistry(pool)), secrets.EnvStore{})
	perms := permission.NewPermissionService(control.NewPostgresPolicyStore(router))
	deps := Deps{Registry: registry(t, "waf.zones", "waf.rules"), Policies: perms, Identity: PgIdentity{DB: pool}}
	b := Bootstrap{Roles: []string{"analyst"}, Grants: []Grant{{Role: "analyst", Entities: []string{"waf.*"}, Actions: []string{"read", "update"}}}}

	res, err := Apply(ctx, deps, tenant, b, Options{})
	require.NoError(t, err)
	assert.Equal(t, 4, res.PoliciesCreated)

	res, err = Apply(ctx, deps, tenant, b, Options{})
	require.NoError(t, err)
	assert.Equal(t, 0, res.PoliciesCreated)
	assert.Equal(t, 4, res.PoliciesExisting)

	rows, err := perms.ListPolicies(ctx, tenant, "waf.zones", true)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}
