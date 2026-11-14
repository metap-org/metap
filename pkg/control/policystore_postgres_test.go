//go:build integration

package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

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
