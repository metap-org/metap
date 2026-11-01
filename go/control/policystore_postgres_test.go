package control

// Ported from the Rust metap-control crate's tests/postgres_policy_store.rs.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
)

func TestCreateListAndDeleteRoundTripAgainstRealPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPostgresPolicyStore(testRouter(pool))
	tenantID := uuid.New() // isolated per test run, nothing to collide with

	subject := permission.SubjectContext
	created, err := store.CreatePolicy(ctx, tenantID, "crm.customers", "read", []string{"sales"}, true,
		nil, nil, "", &subject, permission.EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	if created.Entity != "crm.customers" || created.Action != "read" {
		t.Fatalf("got %+v", created)
	}
	if len(created.Roles) != 1 || created.Roles[0] != "sales" {
		t.Fatalf("got roles %v", created.Roles)
	}

	contextPolicies, err := store.FindContextPolicies(ctx, tenantID, "crm.customers", "read")
	if err != nil {
		t.Fatal(err)
	}
	if len(contextPolicies) != 1 || contextPolicies[0].ID != created.ID {
		t.Fatalf("got %+v", contextPolicies)
	}

	all, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d", len(all))
	}

	listed, err := store.ListPolicies(ctx, tenantID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d", len(listed))
	}

	if err := store.DeletePolicy(ctx, tenantID, created.ID); err != nil {
		t.Fatal(err)
	}

	afterDelete, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("expected no policies after delete, got %d", len(afterDelete))
	}
}

func TestCreatePolicyWithConditionRoundTripsJsonb(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPostgresPolicyStore(testRouter(pool))
	tenantID := uuid.New()

	var condition permission.PolicyCondition
	if err := json.Unmarshal([]byte(`{"attribute":"ownerId","op":"eq","value":{"fromContext":"userId"}}`), &condition); err != nil {
		t.Fatal(err)
	}

	subject := permission.SubjectRecord
	created, err := store.CreatePolicy(ctx, tenantID, "crm.customers", "update", nil, false,
		&condition, nil, "", &subject, permission.EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	if created.Condition == nil {
		t.Fatal("expected a condition")
	}
	if created.Subject != "record" {
		t.Fatalf("got subject %q", created.Subject)
	}

	reloaded, err := store.LoadAllPolicies(ctx, tenantID, "crm.customers")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 || reloaded[0].Condition == nil {
		t.Fatalf("got %+v", reloaded)
	}

	if err := store.DeletePolicy(ctx, tenantID, created.ID); err != nil {
		t.Fatal(err)
	}
}
