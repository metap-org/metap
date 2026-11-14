//go:build integration

// E2E tests running the full CrudService lifecycle against a real Postgres. Ported from the
// Rust metap-crud crate's tests/crud_service_postgres.rs — a reduced-but-real subset: the full
// create/get/update/transition/delete lifecycle, tenant-scoped list, single- and composite-field
// unique violations, the reference-integrity delete guard, computed-field recomputation, and the
// opt-in audit trail. The Rust file's two "sustained_concurrent_*" manual load-test benchmarks
// and its deep audit-masking variants (4 scenarios) are deliberately not ported here — they are
// soak/stress tooling and repeated coverage of the same masking mechanism, not additional
// regression risk for this port.
package crud

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
	"github.com/metap-org/metap/pkg/reconciler"
)

const (
	ordersTable        = "entities.test_orders"
	auditedOrdersTable = "entities.test_audited_orders"
	parentsTable       = "entities.test_parents"
	childrenTable      = "entities.test_children"
	peopleTable        = "entities.test_people"
	apiKeysTable       = "entities.test_api_keys"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	ensureCoreTestTables(t, pool)
	return pool
}

func ensureCoreTestTables(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()
	must := func(err error) {
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	must(err)
}

// reconcileEntities creates (or converges) each entity's table the way production does, through
// the reconciler, so a test never hand-rolls a table shape that could drift from what Compile builds.
// Referenced entities may come after the ones that reference them, so it retries until no progress.
func reconcileEntities(pool *pgxpool.Pool, entities ...metadata.EntityDefinition) {
	pending := entities
	for len(pending) > 0 {
		var failed []metadata.EntityDefinition
		var lastErr error
		for _, e := range pending {
			if _, err := reconciler.ReconcileWithScope(context.Background(), pool, uuid.Nil, e, nil, reconciler.ScopeAllTenants); err != nil {
				failed = append(failed, e)
				lastErr = err
			}
		}
		if len(failed) == len(pending) {
			panic(lastErr)
		}
		pending = failed
	}
}

func cleanupTenant(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) {
	ctx := context.Background()
	for _, table := range []string{ordersTable, auditedOrdersTable, parentsTable, childrenTable, peopleTable, apiKeysTable} {
		pool.Exec(ctx, `DELETE FROM metadata.outbox_events WHERE aggregate_id IN (SELECT id FROM `+table+` WHERE tenant_id = $1)`, tenantID)
	}
	pool.Exec(ctx, `DELETE FROM metadata.workflow_events WHERE tenant_id = $1`, tenantID)
	pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenantID)
	for _, table := range []string{ordersTable, auditedOrdersTable, parentsTable, childrenTable, peopleTable, apiKeysTable} {
		pool.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID)
	}
	pool.Exec(ctx, `DELETE FROM metadata.policies WHERE tenant_id = $1`, tenantID)
}

// testRouter: no control.tenants row is ever inserted by these tests, so Router.Begin always
// takes the unregistered-tenant fallback (public schema) — same behavior CrudService had before
// Router existed.
func testRouter(pool *pgxpool.Pool) *control.Router {
	registry := control.NewPostgresTenantRegistry(pool)
	return control.NewRouter(pool, control.NewRegistryCache(registry), control.EnvStore{})
}

func testPermissions(pool *pgxpool.Pool) *permission.PermissionService {
	return permission.NewPermissionService(control.NewPostgresPolicyStore(testRouter(pool)))
}

func adminContext(tenantID uuid.UUID) *permission.RequestContext {
	return &permission.RequestContext{
		TenantID: tenantID.String(),
		UserID:   uuid.New().String(),
		Roles:    []string{"admin"},
		HasRoles: true,
	}
}

func newCrudService(pool *pgxpool.Pool, entities ...metadata.EntityDefinition) *CrudService {
	reconcileEntities(pool, entities...)
	reg := metadata.NewMetadataRegistry()
	for _, e := range entities {
		if err := reg.Register(e); err != nil {
			panic(err)
		}
	}
	var ptr atomic.Pointer[metadata.MetadataRegistry]
	ptr.Store(reg)
	return NewCrudService(testRouter(pool), &ptr, testPermissions(pool))
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func boolPtr(b bool) *bool { return &b }

func testOrderEntity() metadata.EntityDefinition {
	amountEq100 := permission.AttributeCondition("amount", permission.OpEq, permission.LiteralValue(raw(100)))
	resolutionSet := permission.AttributeCondition("resolution", permission.OpNeq, permission.LiteralValue(raw(nil)))
	return metadata.EntityDefinition{
		Name: "test.orders", Label: "Order", TableName: ordersTable,
		Fields: []metadata.EntityField{
			{Name: "name", Label: "Name", Kind: metadata.KindString, Required: boolPtr(true)},
			{Name: "amount", Label: "Amount", Kind: metadata.KindNumber},
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "approved", "closed"}},
			{Name: "resolution", Label: "Resolution", Kind: metadata.KindString},
			{Name: "closedBy", Label: "Closed By", Kind: metadata.KindString},
		},
		Workflow: &metadata.EntityWorkflow{
			StateField: "status", InitialState: "draft", TerminalStates: []string{"approved"},
			Transitions: []metadata.WorkflowTransition{
				{Action: "approve", From: "draft", To: "approved", Label: "Approve", Guard: &amountEq100},
				{
					Action: "close", From: "approved", To: "closed", Label: "Close",
					Validator: &resolutionSet,
					SetFields: map[string]permission.PolicyValue{"closedBy": permission.FromContextValue("userId")},
				},
			},
		},
	}
}

func auditedOrderEntity() metadata.EntityDefinition {
	e := testOrderEntity()
	e.Name = "test.audited_orders"
	e.TableName = auditedOrdersTable
	e.Audit = &metadata.EntityAuditConfig{Enabled: true}
	return e
}

func parentEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.parents", Label: "Parent", TableName: parentsTable,
		Fields: []metadata.EntityField{{Name: "name", Label: "Name", Kind: metadata.KindString}},
	}
}

func childEntity() metadata.EntityDefinition {
	refEntity := "test.parents"
	refDisplay := "name"
	return metadata.EntityDefinition{
		Name: "test.children", Label: "Child", TableName: childrenTable,
		Fields: []metadata.EntityField{
			{Name: "parentId", Label: "Parent", Kind: metadata.KindReference, RefEntity: &refEntity, RefDisplayField: &refDisplay},
		},
	}
}

func computedPersonEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.people", Label: "Person", TableName: peopleTable,
		Fields: []metadata.EntityField{
			{Name: "firstName", Label: "First Name", Kind: metadata.KindString},
			{Name: "lastName", Label: "Last Name", Kind: metadata.KindString},
			{
				Name: "displayName", Label: "Display Name", Kind: metadata.KindString,
				Computed: &metadata.ComputedSpec{Expression: "{firstName} {lastName}", DependsOn: []string{"firstName", "lastName"}},
			},
		},
	}
}

func TestFullLifecycleCreateGetUpdateTransitionDelete(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, testOrderEntity())

	created, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("First order"), "amount": raw(50)}, rc, nil)
	require.NoError(t, err, "create failed: %v %+v", err, created)
	require.NoError(t, err)
	require.NotNil(t, created.Status, "expected initial status draft, got %+v", created.Status)
	require.Equal(t, "draft", *created.Status, "expected initial status draft, got %+v", created.Status)
	require.EqualValues(t, 1, created.Version, "expected version 1, got %d", created.Version)

	// create validation failure: missing required "name"
	bad, err := crud.Create(ctx, "test.orders", JsonObject{"amount": raw(1)}, rc, nil)
	var badErr *ServiceError
	require.ErrorAs(t, err, &badErr)
	require.EqualValues(t, 400, badErr.Status, "expected validation_failed, got %+v", bad)
	require.Equal(t, "validation_failed", badErr.Code, "expected validation_failed, got %+v", bad)
	_, ok := badErr.FieldErrors["name"]
	require.True(t, ok, "expected name in field errors, got %v", badErr.FieldErrors)

	// get
	got, err := crud.Get(ctx, "test.orders", created.ID, rc)
	require.NoError(t, err, "get failed: %v %+v", err, got)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.Record.ID, "id mismatch")
	require.True(t, got.Capabilities.CanUpdate, "expected can_update true")
	require.Len(t, got.Capabilities.Transitions, 1, "got %+v", got.Capabilities.Transitions)
	require.Equal(t, "approve", got.Capabilities.Transitions[0].Action, "got %+v", got.Capabilities.Transitions)
	require.False(t, got.Capabilities.Transitions[0].Available, "guard requires amount == 100, current is 50")

	// update with stale version -> 409
	stale, err := crud.Update(ctx, "test.orders", created.ID, 999, JsonObject{"amount": raw(100)}, rc, nil)
	var staleErr *ServiceError
	require.ErrorAs(t, err, &staleErr)
	require.EqualValues(t, 409, staleErr.Status, "expected version_conflict, got %+v", stale)
	require.Equal(t, "version_conflict", staleErr.Code, "expected version_conflict, got %+v", stale)

	// update with correct version -> succeeds
	updated, err := crud.Update(ctx, "test.orders", created.ID, created.Version, JsonObject{"amount": raw(100)}, rc, nil)
	require.NoError(t, err, "update failed: %v %+v", err, updated)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version, "expected version 2, got %d", updated.Version)
	var amount float64
	json.Unmarshal(updated.Data["amount"], &amount)
	require.EqualValues(t, 100, amount, "expected amount 100, got %v", amount)
	var status string
	json.Unmarshal(updated.Data["status"], &status)
	require.Equal(t, "draft", status, "status must not change via update, got %q", status)

	// transition guard now passes
	transitioned, err := crud.Transition(ctx, "test.orders", created.ID, "approve", updated.Version, nil, rc, nil)
	require.NoError(t, err, "transition failed: %v %+v", err, transitioned)
	require.NoError(t, err)
	require.NotNil(t, transitioned.Status, "expected approved, got %+v", transitioned.Status)
	require.Equal(t, "approved", *transitioned.Status, "expected approved, got %+v", transitioned.Status)
	require.EqualValues(t, 3, transitioned.Version, "expected version 3, got %d", transitioned.Version)

	// transition again from a now-invalid from-state
	invalid, err := crud.Transition(ctx, "test.orders", created.ID, "approve", transitioned.Version, nil, rc, nil)
	var invalidErr *ServiceError
	require.ErrorAs(t, err, &invalidErr)
	require.EqualValues(t, 409, invalidErr.Status, "expected invalid_transition, got %+v", invalid)
	require.Equal(t, "invalid_transition", invalidErr.Code, "expected invalid_transition, got %+v", invalid)

	// delete (soft)
	deleted, err := crud.Delete(ctx, "test.orders", created.ID, transitioned.Version, rc, nil)
	require.NoError(t, err, "delete failed: %v %+v", err, deleted)
	require.NoError(t, err)
	require.EqualValues(t, 4, deleted.Version, "expected version 4, got %d", deleted.Version)

	// get after delete -> 404
	afterDelete, err := crud.Get(ctx, "test.orders", created.ID, rc)
	var afterDeleteErr *ServiceError
	require.ErrorAs(t, err, &afterDeleteErr)
	require.EqualValues(t, 404, afterDeleteErr.Status, "expected record_not_found, got %+v", afterDelete)
	require.Equal(t, "record_not_found", afterDeleteErr.Code, "expected record_not_found, got %+v", afterDelete)

	var eventCount int64
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.workflow_events WHERE tenant_id = $1 AND record_id = $2`, tenantID, created.ID).Scan(&eventCount)
	require.EqualValues(t, 1, eventCount, "expected 1 workflow event, got %d", eventCount)

	rows, err := pool.Query(ctx, `SELECT topic FROM metadata.outbox_events WHERE aggregate_id = $1 ORDER BY created_at`, created.ID)
	require.NoError(t, err)
	var topics []string
	for rows.Next() {
		var topic string
		rows.Scan(&topic)
		topics = append(topics, topic)
	}
	rows.Close()
	want := []string{"test.orders.record.created", "test.orders.record.updated", "test.orders.workflow.transitioned", "test.orders.record.deleted"}
	require.Len(t, topics, len(want), "got topics %v, want %v", topics, want)
	for i := range want {
		require.Equal(t, want[i], topics[i], "got topics %v, want %v", topics, want)
	}
}

func TestListReturnsCreatedRecordsScopedToTenant(t *testing.T) {
	pool := testPool(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	defer cleanupTenant(t, pool, tenantA)
	defer cleanupTenant(t, pool, tenantB)
	ctx := context.Background()
	crud := newCrudService(pool, testOrderEntity())

	rcA := adminContext(tenantA)
	rcB := adminContext(tenantB)
	for i := 0; i < 3; i++ {
		r, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("A order")}, rcA, nil)
		require.NoError(t, err, "create failed: %v %+v", err, r)
		require.NoError(t, err)
	}
	if r, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("B order")}, rcB, nil); err != nil {
		require.FailNow(t, fmt.Sprintf("create failed: %v %+v", err, r))
	}

	listed, err := crud.List(ctx, "test.orders", query.ListInput{Limit: 50}, rcA)
	require.NoError(t, err, "list failed: %v %+v", err, listed)
	require.NoError(t, err)
	require.Len(t, listed.Data, 3, "expected 3 records scoped to tenant A, got %d", len(listed.Data))
}

func TestUniqueFieldViolationOnADedicatedTableNamesTheField(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	pool.Exec(ctx, `DROP TABLE IF EXISTS entities.test_unique_widgets CASCADE`)
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP TABLE IF EXISTS entities.test_unique_widgets CASCADE`) })

	entity := metadata.EntityDefinition{
		Name: "test.unique_widgets", Label: "Unique Widget", TableName: "entities.test_unique_widgets",
		Fields: []metadata.EntityField{{Name: "sku", Label: "SKU", Kind: metadata.KindString, Required: boolPtr(true), Unique: boolPtr(true)}},
	}
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	rc := adminContext(tenantID)
	crud := newCrudService(pool, entity)

	first, err := crud.Create(ctx, "test.unique_widgets", JsonObject{"sku": raw("SKU-1")}, rc, nil)
	require.NoError(t, err, "first create failed: %v %+v", err, first)
	require.NoError(t, err)
	dup, err := crud.Create(ctx, "test.unique_widgets", JsonObject{"sku": raw("SKU-1")}, rc, nil)
	var dupErr *ServiceError
	require.ErrorAs(t, err, &dupErr)
	require.EqualValues(t, 409, dupErr.Status, "expected unique_violation, got %+v", dup)
	require.Equal(t, "unique_violation", dupErr.Code, "expected unique_violation, got %+v", dup)
	_, ok := dupErr.FieldErrors["sku"]
	require.True(t, ok, "expected sku named in field errors, got %v", dupErr.FieldErrors)
}

func TestCompositeUniqueFieldViolationNamesEveryFieldInTheConstraint(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	pool.Exec(ctx, `DROP TABLE IF EXISTS entities.test_unique_list_entries CASCADE`)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DROP TABLE IF EXISTS entities.test_unique_list_entries CASCADE`)
	})

	entity := metadata.EntityDefinition{
		Name: "test.unique_list_entries", Label: "Unique List Entry", TableName: "entities.test_unique_list_entries",
		Fields: []metadata.EntityField{
			{Name: "type", Label: "Type", Kind: metadata.KindString, Required: boolPtr(true)},
			{Name: "value", Label: "Value", Kind: metadata.KindString, Required: boolPtr(true)},
		},
		UniqueConstraints: []metadata.EntityUniqueConstraint{{Fields: []string{"type", "value"}}},
	}
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	rc := adminContext(tenantID)
	crud := newCrudService(pool, entity)

	first, err := crud.Create(ctx, "test.unique_list_entries", JsonObject{"type": raw("blacklist"), "value": raw("1.2.3.4")}, rc, nil)
	require.NoError(t, err, "first create failed: %v %+v", err, first)
	require.NoError(t, err)
	dup, err := crud.Create(ctx, "test.unique_list_entries", JsonObject{"type": raw("blacklist"), "value": raw("1.2.3.4")}, rc, nil)
	var dupErr *ServiceError
	require.ErrorAs(t, err, &dupErr)
	require.EqualValues(t, 409, dupErr.Status, "expected unique_violation, got %+v", dup)
	require.Equal(t, "unique_violation", dupErr.Code, "expected unique_violation, got %+v", dup)
	_, ok := dupErr.FieldErrors["type"]
	require.True(t, ok, "expected type named, got %v", dupErr.FieldErrors)
	_, ok = dupErr.FieldErrors["value"]
	require.True(t, ok, "expected value named, got %v", dupErr.FieldErrors)
}

func TestDeleteIsRejectedWhenAnotherRecordStillReferencesItThenSucceedsOnceItIsGone(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, parentEntity(), childEntity())

	parent, err := crud.Create(ctx, "test.parents", JsonObject{"name": raw("Acme")}, rc, nil)
	require.NoError(t, err, "create parent failed: %v %+v", err, parent)
	require.NoError(t, err)
	child, err := crud.Create(ctx, "test.children", JsonObject{"parentId": raw(parent.ID.String())}, rc, nil)
	require.NoError(t, err, "create child failed: %v %+v", err, child)
	require.NoError(t, err)

	blocked, err := crud.Delete(ctx, "test.parents", parent.ID, parent.Version, rc, nil)
	var blockedErr *ServiceError
	require.ErrorAs(t, err, &blockedErr)
	require.EqualValues(t, 409, blockedErr.Status, "expected record_referenced, got %+v", blocked)
	require.Equal(t, "record_referenced", blockedErr.Code, "expected record_referenced, got %+v", blocked)
	_, ok := blockedErr.FieldErrors["test.children.parentId"]
	require.True(t, ok, "expected test.children.parentId blocker named, got %v", blockedErr.FieldErrors)

	r, err := crud.Delete(ctx, "test.children", child.ID, child.Version, rc, nil)
	require.NoError(t, err, "delete child failed: %v %+v", err, r)
	require.NoError(t, err)

	unblocked, err := crud.Delete(ctx, "test.parents", parent.ID, parent.Version, rc, nil)
	require.NoError(t, err, "expected delete to succeed once child is gone, got %v %+v", err, unblocked)
	require.NoError(t, err)
}

func TestListHydratesRelatedDisplayForReferenceFieldsWithDisplayField(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, parentEntity(), childEntity())

	parent, err := crud.Create(ctx, "test.parents", JsonObject{"name": raw("Acme Corp")}, rc, nil)
	require.NoError(t, err, "create parent failed: %v %+v", err, parent)
	require.NoError(t, err)
	r, err := crud.Create(ctx, "test.children", JsonObject{"parentId": raw(parent.ID.String())}, rc, nil)
	require.NoError(t, err, "create child failed: %v %+v", err, r)
	require.NoError(t, err)

	listed, err := crud.List(ctx, "test.children", query.ListInput{Limit: 50}, rc)
	require.NoError(t, err, "list failed: %v %+v", err, listed)
	require.NoError(t, err)
	require.Len(t, listed.Data, 1, "expected 1 child, got %d", len(listed.Data))
	require.Equal(t, "Acme Corp", listed.Data[0].RelatedDisplay["parentId"], "expected hydrated display value, got %+v", listed.Data[0].RelatedDisplay)
}

func TestComputedFieldIsRecalculatedOnCreateAndUpdate(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, computedPersonEntity())

	created, err := crud.Create(ctx, "test.people", JsonObject{"firstName": raw("Ada"), "lastName": raw("Lovelace")}, rc, nil)
	require.NoError(t, err, "create failed: %v %+v", err, created)
	require.NoError(t, err)
	var displayName string
	json.Unmarshal(created.Data["displayName"], &displayName)
	require.Equal(t, "Ada Lovelace", displayName, "expected computed displayName, got %q", displayName)

	updated, err := crud.Update(ctx, "test.people", created.ID, created.Version, JsonObject{"lastName": raw("Byron")}, rc, nil)
	require.NoError(t, err, "update failed: %v %+v", err, updated)
	require.NoError(t, err)
	json.Unmarshal(updated.Data["displayName"], &displayName)
	require.Equal(t, "Ada Byron", displayName, "expected recomputed displayName, got %q", displayName)
}

func TestAuditedEntityWritesAreRecordedAndUnauditedEntityWritesAreNot(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)

	reg := metadata.NewMetadataRegistry()
	for _, e := range []metadata.EntityDefinition{testOrderEntity(), auditedOrderEntity()} {
		reconcileEntities(pool, e)
		require.NoError(t, reg.Register(e))
	}
	var ptr atomic.Pointer[metadata.MetadataRegistry]
	ptr.Store(reg)
	store := audit.NewPostgresStore(pool)
	crud := WithAudit(testRouter(pool), &ptr, testPermissions(pool), store)

	unaudited, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("plain")}, rc, nil)
	require.NoError(t, err, "create failed: %v %+v", err, unaudited)
	require.NoError(t, err)
	audited, err := crud.Create(ctx, "test.audited_orders", JsonObject{"name": raw("audited")}, rc, nil)
	require.NoError(t, err, "create failed: %v %+v", err, audited)
	require.NoError(t, err)

	var plainCount, auditedCount int64
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2`, tenantID, unaudited.ID).Scan(&plainCount)
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2`, tenantID, audited.ID).Scan(&auditedCount)
	require.EqualValues(t, 0, plainCount, "expected no audit row for unaudited entity, got %d", plainCount)
	require.EqualValues(t, 1, auditedCount, "expected exactly 1 audit row for audited entity, got %d", auditedCount)

	events, err := crud.ListAuditEvents(ctx, "test.audited_orders", audited.ID, rc)
	require.NoError(t, err, "list audit events failed: %v %+v", err, events)
	require.NoError(t, err)
	require.Len(t, events, 1, "got %+v", events)
	require.Equal(t, "create", events[0].Action, "got %+v", events)
}

func apiKeyEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.api_keys", Label: "API key", TableName: apiKeysTable,
		Fields: []metadata.EntityField{
			{Name: "name", Label: "Name", Kind: metadata.KindString},
			{Name: "lastUsedAt", Label: "Last used", Kind: metadata.KindDatetime, System: boolPtr(true)},
		},
		Audit: &metadata.EntityAuditConfig{Enabled: true},
	}
}

// System fields: users cannot write them, SetSystemFields writes them without bumping version,
// emitting outbox or auditing, and a later user Update keeps the stored system value.
func TestSystemFieldsAreWrittenOnlyBySetSystemFieldsWithoutVersionBumpOrAudit(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)

	reg := metadata.NewMetadataRegistry()
	reconcileEntities(pool, apiKeyEntity())
	require.NoError(t, reg.Register(apiKeyEntity()))
	var ptr atomic.Pointer[metadata.MetadataRegistry]
	ptr.Store(reg)
	crud := WithAudit(testRouter(pool), &ptr, testPermissions(pool), audit.NewPostgresStore(pool))

	rejected, err := crud.Create(ctx, "test.api_keys", JsonObject{"name": raw("ci"), "lastUsedAt": raw("2026-01-01T00:00:00Z")}, rc, nil)
	var rejectedErr *ServiceError
	require.ErrorAs(t, err, &rejectedErr)
	require.NotNil(t, rejectedErr.FieldErrors["lastUsedAt"], "create with a system field must be rejected: %v %+v", err, rejected)
	created, err := crud.Create(ctx, "test.api_keys", JsonObject{"name": raw("ci")}, rc, nil)
	require.NoError(t, err, "create failed: %v %+v", err, created)
	require.NoError(t, err)
	id, versionBefore := created.ID, created.Version

	countAudit := func() int64 {
		var n int64
		pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2`, tenantID, id).Scan(&n)
		return n
	}
	countOutbox := func() int64 {
		var n int64
		pool.QueryRow(ctx, `SELECT count(*) FROM metadata.outbox_events WHERE aggregate_id = $1`, id).Scan(&n)
		return n
	}
	auditBefore, outboxBefore := countAudit(), countOutbox()

	found, err := crud.SetSystemFields(ctx, tenantID, "test.api_keys", id, JsonObject{"lastUsedAt": raw("2026-11-06T10:00:00Z")})
	require.NoError(t, err, "SetSystemFields: found=%v err=", found)
	require.True(t, found, "SetSystemFields: found=%v err=%v", found, err)
	_, err = crud.SetSystemFields(ctx, tenantID, "test.api_keys", id, JsonObject{"name": raw("x")})
	require.Error(t, err, "SetSystemFields must refuse a non-system field")
	got, err := crud.Get(ctx, "test.api_keys", id, rc)
	require.NoError(t, err, "get failed: %v %+v", err, got)
	require.NoError(t, err)
	require.Equal(t, versionBefore, got.Record.Version, "version must not change: before %d after %d", versionBefore, got.Record.Version)
	// A datetime column reads back in Postgres's own ISO form (offset, not Z); it is the same instant.
	require.Equal(t, `"2026-11-06T10:00:00+00:00"`, string(got.Record.Data["lastUsedAt"]), "got %s", got.Record.Data["lastUsedAt"])
	require.Equal(t, auditBefore, countAudit(), "SetSystemFields must not audit or emit outbox events")
	require.Equal(t, outboxBefore, countOutbox(), "SetSystemFields must not audit or emit outbox events")

	_, err = crud.Update(ctx, "test.api_keys", id, versionBefore, JsonObject{"lastUsedAt": raw("2020-01-01T00:00:00Z")}, rc, nil)
	var rErr *ServiceError
	require.ErrorAs(t, err, &rErr)
	// A system write before the user's update must survive it. (The interleaved case, a system
	// write landing between Update's precheck read and its UPDATE, is covered by the UPDATE's own
	// SQL re-reading system keys from the stored row; it is not deterministic enough to race here.)
	_, err = crud.SetSystemFields(ctx, tenantID, "test.api_keys", id, JsonObject{"lastUsedAt": raw("2026-11-06T11:00:00Z")})
	require.NoError(t, err)
	updated, err := crud.Update(ctx, "test.api_keys", id, versionBefore, JsonObject{"name": raw("ci-renamed")}, rc, nil)
	require.NoError(t, err, "update failed: %v %+v", err, updated)
	require.NoError(t, err)
	require.Equal(t, `"2026-11-06T11:00:00+00:00"`, string(updated.Data["lastUsedAt"]), "user update must keep the stored system value, got %s", updated.Data["lastUsedAt"])
}
