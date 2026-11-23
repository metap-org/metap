//go:build integration

package policypg

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/secrets"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	return testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
}

func testRouter(pool *pgxpool.Pool) *tenancy.Router {
	return tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), secrets.EnvStore{})
}

func TestCreateListAndDeleteRoundTripAgainstRealPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPostgresPolicyStore(testRouter(pool))
	tenantID := uuid.New() // isolated per test run, nothing to collide with

	subject := permission.SubjectContext
	created, err := store.CreatePolicy(ctx, tenantID, "crm.customers", "read", []string{"sales"}, true,
		nil, nil, "", &subject, permission.EffectAllow)
	require.NoError(t, err)
	require.Equal(t, "crm.customers", created.Entity, "got %+v", created)
	require.Equal(t, "read", created.Action, "got %+v", created)
	require.Len(t, created.Roles, 1, "got roles %v", created.Roles)
	require.Equal(t, "sales", created.Roles[0], "got roles %v", created.Roles)

	contextPolicies, err := store.FindContextPolicies(ctx, tenantID, "crm.customers", "read")
	require.NoError(t, err)
	require.Len(t, contextPolicies, 1, "got %+v", contextPolicies)
	require.Equal(t, created.ID, contextPolicies[0].ID, "got %+v", contextPolicies)

	all, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	require.NoError(t, err)
	require.Len(t, all, 1, "got %d", len(all))

	listed, err := store.ListPolicies(ctx, tenantID, "", false)
	require.NoError(t, err)
	require.Len(t, listed, 1, "got %d", len(listed))

	require.NoError(t, store.DeletePolicy(ctx, tenantID, created.ID))

	afterDelete, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	require.NoError(t, err)
	require.Len(t, afterDelete, 0, "expected no policies after delete, got %d", len(afterDelete))
}

func TestCreatePolicyWithConditionRoundTripsJsonb(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPostgresPolicyStore(testRouter(pool))
	tenantID := uuid.New()

	var condition permission.PolicyCondition
	require.NoError(t, json.Unmarshal([]byte(`{"attribute":"ownerId","op":"eq","value":{"fromContext":"userId"}}`), &condition))

	subject := permission.SubjectRecord
	created, err := store.CreatePolicy(ctx, tenantID, "crm.customers", "update", nil, false,
		&condition, nil, "", &subject, permission.EffectAllow)
	require.NoError(t, err)
	require.NotNil(t, created.Condition, "expected a condition")
	require.Equal(t, "record", created.Subject, "got subject %q", created.Subject)

	reloaded, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	require.NoError(t, err)
	require.Len(t, reloaded, 1, "got %+v", reloaded)
	require.NotNil(t, reloaded[0].Condition, "got %+v", reloaded)

	require.NoError(t, store.DeletePolicy(ctx, tenantID, created.ID))
}

// LoadContextPolicies is what MatrixStore holds in memory: it must return exactly the rows
// FindContextPolicies returns, entity by entity and action by action, and nothing else (no
// field-level or record-level policy, nobody else's tenant).
func TestLoadContextPoliciesMatchesFindContextPolicies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPostgresPolicyStore(testRouter(pool))
	tenantID, otherTenant := uuid.New(), uuid.New()

	contextSubject, recordSubject := permission.SubjectContext, permission.SubjectRecord
	create := func(tenant uuid.UUID, entity, action string, roles []string, field string, subject *permission.PolicySubject, effect permission.PolicyEffect) {
		t.Helper()
		_, err := store.CreatePolicy(ctx, tenant, entity, action, roles, roles != nil, nil, nil, field, subject, effect)
		require.NoError(t, err)
	}
	create(tenantID, "waf.zones", "read", []string{"viewer"}, "", &contextSubject, permission.EffectAllow)
	create(tenantID, "waf.zones", "update", []string{"analyst"}, "", &contextSubject, permission.EffectAllow)
	create(tenantID, "waf.zones", "update", []string{"intern"}, "", &contextSubject, permission.EffectDeny)
	create(tenantID, permission.ResourceKey("waf.requests"), "view", []string{"analyst"}, "", &contextSubject, permission.EffectAllow)
	create(tenantID, "waf.zones", "read", []string{"viewer"}, "secret", &contextSubject, permission.EffectDeny) // field-level: not entity-level
	create(tenantID, "waf.zones", "update", []string{"viewer"}, "", &recordSubject, permission.EffectAllow)     // record subject: not entity-level
	create(otherTenant, "waf.zones", "read", []string{"stranger"}, "", &contextSubject, permission.EffectAllow) // another tenant

	loaded, err := store.LoadContextPolicies(ctx, tenantID)
	require.NoError(t, err)
	require.Len(t, loaded, 4, "got %+v", loaded)

	byKey := map[[2]string][]uuid.UUID{}
	for _, r := range loaded {
		k := [2]string{r.Entity, r.Action}
		byKey[k] = append(byKey[k], r.ID)
	}
	for k, ids := range byKey {
		found, err := store.FindContextPolicies(ctx, tenantID, k[0], k[1])
		require.NoError(t, err)
		require.Len(t, found, len(ids), "%v: the matrix and the per-key query disagree", k)
		for _, f := range found {
			require.Contains(t, ids, f.ID)
		}
	}
}
