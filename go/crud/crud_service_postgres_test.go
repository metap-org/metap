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
	"os"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/audit"
	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
)

const (
	ordersTable        = "entities.test_orders"
	auditedOrdersTable = "entities.test_audited_orders"
	parentsTable       = "entities.test_parents"
	childrenTable      = "entities.test_children"
	peopleTable        = "entities.test_people"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	ensureCoreTestTables(t, pool)
	t.Cleanup(pool.Close)
	return pool
}

func ensureCoreTestTables(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	must(err)
	for _, table := range []string{ordersTable, auditedOrdersTable, parentsTable, childrenTable, peopleTable} {
		_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
			tenant_id uuid NOT NULL,
			code varchar(120),
			status varchar(80),
			data jsonb DEFAULT '{}'::jsonb NOT NULL,
			version integer DEFAULT 1 NOT NULL,
			deleted boolean DEFAULT false NOT NULL,
			created_at timestamptz DEFAULT now() NOT NULL,
			updated_at timestamptz DEFAULT now() NOT NULL,
			created_by uuid,
			updated_by uuid
		)`)
		must(err)
	}
	ensureReferenceSyncTrigger(t, pool, childrenTable, "parentId")
}

// ensureReferenceSyncTrigger hand-rolls what metap-reconciler builds in production for a
// Reference field with ref_entity set — a real column plus a BEFORE INSERT OR UPDATE trigger
// copying data->>'field' into it, so findReferencingRecords' "field" = $N::uuid query actually
// matches something.
func ensureReferenceSyncTrigger(t *testing.T, pool *pgxpool.Pool, table, field string) {
	ctx := context.Background()
	bareTable := table[len("entities."):]
	_, err := pool.Exec(ctx, `ALTER TABLE `+table+` ADD COLUMN IF NOT EXISTS "`+field+`" uuid`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION sync_`+bareTable+`_`+field+`() RETURNS trigger AS $$
		BEGIN NEW."`+field+`" := (NEW.data ->> '`+field+`')::uuid; RETURN NEW; END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE OR REPLACE TRIGGER trg_sync_`+bareTable+`_`+field+` BEFORE INSERT OR UPDATE ON `+table+`
		FOR EACH ROW EXECUTE FUNCTION sync_`+bareTable+`_`+field+`()`)
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupTenant(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) {
	ctx := context.Background()
	for _, table := range []string{ordersTable, auditedOrdersTable, parentsTable, childrenTable, peopleTable} {
		pool.Exec(ctx, `DELETE FROM metadata.outbox_events WHERE aggregate_id IN (SELECT id FROM `+table+` WHERE tenant_id = $1)`, tenantID)
	}
	pool.Exec(ctx, `DELETE FROM metadata.workflow_events WHERE tenant_id = $1`, tenantID)
	pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenantID)
	for _, table := range []string{ordersTable, auditedOrdersTable, parentsTable, childrenTable, peopleTable} {
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
	if err != nil || !created.IsOk {
		t.Fatalf("create failed: %v %+v", err, created)
	}
	if created.Data.Status == nil || *created.Data.Status != "draft" {
		t.Fatalf("expected initial status draft, got %+v", created.Data.Status)
	}
	if created.Data.Version != 1 {
		t.Fatalf("expected version 1, got %d", created.Data.Version)
	}

	// create validation failure: missing required "name"
	bad, err := crud.Create(ctx, "test.orders", JsonObject{"amount": raw(1)}, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bad.IsOk || bad.Status != 400 || bad.ErrorCode != "validation_failed" {
		t.Fatalf("expected validation_failed, got %+v", bad)
	}
	if _, ok := bad.FieldErrors["name"]; !ok {
		t.Fatalf("expected name in field errors, got %v", bad.FieldErrors)
	}

	// get
	got, err := crud.Get(ctx, "test.orders", created.Data.ID, rc)
	if err != nil || !got.IsOk {
		t.Fatalf("get failed: %v %+v", err, got)
	}
	if got.Data.Record.ID != created.Data.ID {
		t.Fatalf("id mismatch")
	}
	if !got.Data.Capabilities.CanUpdate {
		t.Fatal("expected can_update true")
	}
	if len(got.Data.Capabilities.Transitions) != 1 || got.Data.Capabilities.Transitions[0].Action != "approve" {
		t.Fatalf("got %+v", got.Data.Capabilities.Transitions)
	}
	if got.Data.Capabilities.Transitions[0].Available {
		t.Fatal("guard requires amount == 100, current is 50")
	}

	// update with stale version -> 409
	stale, err := crud.Update(ctx, "test.orders", created.Data.ID, 999, JsonObject{"amount": raw(100)}, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stale.IsOk || stale.Status != 409 || stale.ErrorCode != "version_conflict" {
		t.Fatalf("expected version_conflict, got %+v", stale)
	}

	// update with correct version -> succeeds
	updated, err := crud.Update(ctx, "test.orders", created.Data.ID, created.Data.Version, JsonObject{"amount": raw(100)}, rc, nil)
	if err != nil || !updated.IsOk {
		t.Fatalf("update failed: %v %+v", err, updated)
	}
	if updated.Data.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Data.Version)
	}
	var amount float64
	json.Unmarshal(updated.Data.Data["amount"], &amount)
	if amount != 100 {
		t.Fatalf("expected amount 100, got %v", amount)
	}
	var status string
	json.Unmarshal(updated.Data.Data["status"], &status)
	if status != "draft" {
		t.Fatalf("status must not change via update, got %q", status)
	}

	// transition guard now passes
	transitioned, err := crud.Transition(ctx, "test.orders", created.Data.ID, "approve", updated.Data.Version, nil, rc, nil)
	if err != nil || !transitioned.IsOk {
		t.Fatalf("transition failed: %v %+v", err, transitioned)
	}
	if transitioned.Data.Status == nil || *transitioned.Data.Status != "approved" {
		t.Fatalf("expected approved, got %+v", transitioned.Data.Status)
	}
	if transitioned.Data.Version != 3 {
		t.Fatalf("expected version 3, got %d", transitioned.Data.Version)
	}

	// transition again from a now-invalid from-state
	invalid, err := crud.Transition(ctx, "test.orders", created.Data.ID, "approve", transitioned.Data.Version, nil, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if invalid.IsOk || invalid.Status != 409 || invalid.ErrorCode != "invalid_transition" {
		t.Fatalf("expected invalid_transition, got %+v", invalid)
	}

	// delete (soft)
	deleted, err := crud.Delete(ctx, "test.orders", created.Data.ID, transitioned.Data.Version, rc, nil)
	if err != nil || !deleted.IsOk {
		t.Fatalf("delete failed: %v %+v", err, deleted)
	}
	if deleted.Data.Version != 4 {
		t.Fatalf("expected version 4, got %d", deleted.Data.Version)
	}

	// get after delete -> 404
	afterDelete, err := crud.Get(ctx, "test.orders", created.Data.ID, rc)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.IsOk || afterDelete.Status != 404 || afterDelete.ErrorCode != "record_not_found" {
		t.Fatalf("expected record_not_found, got %+v", afterDelete)
	}

	var eventCount int64
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.workflow_events WHERE tenant_id = $1 AND record_id = $2`, tenantID, created.Data.ID).Scan(&eventCount)
	if eventCount != 1 {
		t.Fatalf("expected 1 workflow event, got %d", eventCount)
	}

	rows, err := pool.Query(ctx, `SELECT topic FROM metadata.outbox_events WHERE aggregate_id = $1 ORDER BY created_at`, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	var topics []string
	for rows.Next() {
		var topic string
		rows.Scan(&topic)
		topics = append(topics, topic)
	}
	rows.Close()
	want := []string{"test.orders.record.created", "test.orders.record.updated", "test.orders.workflow.transitioned", "test.orders.record.deleted"}
	if len(topics) != len(want) {
		t.Fatalf("got topics %v, want %v", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Fatalf("got topics %v, want %v", topics, want)
		}
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
		if r, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("A order")}, rcA, nil); err != nil || !r.IsOk {
			t.Fatalf("create failed: %v %+v", err, r)
		}
	}
	if r, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("B order")}, rcB, nil); err != nil || !r.IsOk {
		t.Fatalf("create failed: %v %+v", err, r)
	}

	listed, err := crud.List(ctx, "test.orders", query.ListInput{Limit: 50}, rcA)
	if err != nil || !listed.IsOk {
		t.Fatalf("list failed: %v %+v", err, listed)
	}
	if len(listed.Data) != 3 {
		t.Fatalf("expected 3 records scoped to tenant A, got %d", len(listed.Data))
	}
}

func TestUniqueFieldViolationOnADedicatedTableNamesTheField(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	pool.Exec(ctx, `DROP TABLE IF EXISTS entities.test_unique_widgets CASCADE`)
	pool.Exec(ctx, `CREATE TABLE entities.test_unique_widgets (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, code varchar(120), status varchar(80),
		data jsonb NOT NULL DEFAULT '{}'::jsonb, version integer NOT NULL DEFAULT 1, deleted boolean NOT NULL DEFAULT false,
		created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), created_by uuid, updated_by uuid)`)
	pool.Exec(ctx, `CREATE UNIQUE INDEX uniq_test_unique_widgets_sku ON entities.test_unique_widgets ((jsonb_extract_path_text(data, 'sku'))) WHERE deleted = false`)
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
	if err != nil || !first.IsOk {
		t.Fatalf("first create failed: %v %+v", err, first)
	}
	dup, err := crud.Create(ctx, "test.unique_widgets", JsonObject{"sku": raw("SKU-1")}, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dup.IsOk || dup.Status != 409 || dup.ErrorCode != "unique_violation" {
		t.Fatalf("expected unique_violation, got %+v", dup)
	}
	if _, ok := dup.FieldErrors["sku"]; !ok {
		t.Fatalf("expected sku named in field errors, got %v", dup.FieldErrors)
	}
}

func TestCompositeUniqueFieldViolationNamesEveryFieldInTheConstraint(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	pool.Exec(ctx, `DROP TABLE IF EXISTS entities.test_unique_list_entries CASCADE`)
	pool.Exec(ctx, `CREATE TABLE entities.test_unique_list_entries (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, code varchar(120), status varchar(80),
		data jsonb NOT NULL DEFAULT '{}'::jsonb, version integer NOT NULL DEFAULT 1, deleted boolean NOT NULL DEFAULT false,
		created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), created_by uuid, updated_by uuid)`)
	pool.Exec(ctx, `CREATE UNIQUE INDEX uniq_test_unique_list_entries_type_value ON entities.test_unique_list_entries
		((jsonb_extract_path_text(data, 'type')), (jsonb_extract_path_text(data, 'value'))) WHERE deleted = false`)
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
	if err != nil || !first.IsOk {
		t.Fatalf("first create failed: %v %+v", err, first)
	}
	dup, err := crud.Create(ctx, "test.unique_list_entries", JsonObject{"type": raw("blacklist"), "value": raw("1.2.3.4")}, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dup.IsOk || dup.Status != 409 || dup.ErrorCode != "unique_violation" {
		t.Fatalf("expected unique_violation, got %+v", dup)
	}
	if _, ok := dup.FieldErrors["type"]; !ok {
		t.Fatalf("expected type named, got %v", dup.FieldErrors)
	}
	if _, ok := dup.FieldErrors["value"]; !ok {
		t.Fatalf("expected value named, got %v", dup.FieldErrors)
	}
}

func TestDeleteIsRejectedWhenAnotherRecordStillReferencesItThenSucceedsOnceItIsGone(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, parentEntity(), childEntity())

	parent, err := crud.Create(ctx, "test.parents", JsonObject{"name": raw("Acme")}, rc, nil)
	if err != nil || !parent.IsOk {
		t.Fatalf("create parent failed: %v %+v", err, parent)
	}
	child, err := crud.Create(ctx, "test.children", JsonObject{"parentId": raw(parent.Data.ID.String())}, rc, nil)
	if err != nil || !child.IsOk {
		t.Fatalf("create child failed: %v %+v", err, child)
	}

	blocked, err := crud.Delete(ctx, "test.parents", parent.Data.ID, parent.Data.Version, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.IsOk || blocked.Status != 409 || blocked.ErrorCode != "record_referenced" {
		t.Fatalf("expected record_referenced, got %+v", blocked)
	}
	if _, ok := blocked.FieldErrors["test.children.parentId"]; !ok {
		t.Fatalf("expected test.children.parentId blocker named, got %v", blocked.FieldErrors)
	}

	if r, err := crud.Delete(ctx, "test.children", child.Data.ID, child.Data.Version, rc, nil); err != nil || !r.IsOk {
		t.Fatalf("delete child failed: %v %+v", err, r)
	}

	unblocked, err := crud.Delete(ctx, "test.parents", parent.Data.ID, parent.Data.Version, rc, nil)
	if err != nil || !unblocked.IsOk {
		t.Fatalf("expected delete to succeed once child is gone, got %v %+v", err, unblocked)
	}
}

func TestListHydratesRelatedDisplayForReferenceFieldsWithDisplayField(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, parentEntity(), childEntity())

	parent, err := crud.Create(ctx, "test.parents", JsonObject{"name": raw("Acme Corp")}, rc, nil)
	if err != nil || !parent.IsOk {
		t.Fatalf("create parent failed: %v %+v", err, parent)
	}
	if r, err := crud.Create(ctx, "test.children", JsonObject{"parentId": raw(parent.Data.ID.String())}, rc, nil); err != nil || !r.IsOk {
		t.Fatalf("create child failed: %v %+v", err, r)
	}

	listed, err := crud.List(ctx, "test.children", query.ListInput{Limit: 50}, rc)
	if err != nil || !listed.IsOk {
		t.Fatalf("list failed: %v %+v", err, listed)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("expected 1 child, got %d", len(listed.Data))
	}
	if listed.Data[0].RelatedDisplay["parentId"] != "Acme Corp" {
		t.Fatalf("expected hydrated display value, got %+v", listed.Data[0].RelatedDisplay)
	}
}

func TestComputedFieldIsRecalculatedOnCreateAndUpdate(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)
	crud := newCrudService(pool, computedPersonEntity())

	created, err := crud.Create(ctx, "test.people", JsonObject{"firstName": raw("Ada"), "lastName": raw("Lovelace")}, rc, nil)
	if err != nil || !created.IsOk {
		t.Fatalf("create failed: %v %+v", err, created)
	}
	var displayName string
	json.Unmarshal(created.Data.Data["displayName"], &displayName)
	if displayName != "Ada Lovelace" {
		t.Fatalf("expected computed displayName, got %q", displayName)
	}

	updated, err := crud.Update(ctx, "test.people", created.Data.ID, created.Data.Version, JsonObject{"lastName": raw("Byron")}, rc, nil)
	if err != nil || !updated.IsOk {
		t.Fatalf("update failed: %v %+v", err, updated)
	}
	json.Unmarshal(updated.Data.Data["displayName"], &displayName)
	if displayName != "Ada Byron" {
		t.Fatalf("expected recomputed displayName, got %q", displayName)
	}
}

func TestAuditedEntityWritesAreRecordedAndUnauditedEntityWritesAreNot(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	defer cleanupTenant(t, pool, tenantID)
	ctx := context.Background()
	rc := adminContext(tenantID)

	reg := metadata.NewMetadataRegistry()
	for _, e := range []metadata.EntityDefinition{testOrderEntity(), auditedOrderEntity()} {
		if err := reg.Register(e); err != nil {
			t.Fatal(err)
		}
	}
	var ptr atomic.Pointer[metadata.MetadataRegistry]
	ptr.Store(reg)
	store := audit.NewPostgresStore(pool)
	crud := WithAudit(testRouter(pool), &ptr, testPermissions(pool), store)

	unaudited, err := crud.Create(ctx, "test.orders", JsonObject{"name": raw("plain")}, rc, nil)
	if err != nil || !unaudited.IsOk {
		t.Fatalf("create failed: %v %+v", err, unaudited)
	}
	audited, err := crud.Create(ctx, "test.audited_orders", JsonObject{"name": raw("audited")}, rc, nil)
	if err != nil || !audited.IsOk {
		t.Fatalf("create failed: %v %+v", err, audited)
	}

	var plainCount, auditedCount int64
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2`, tenantID, unaudited.Data.ID).Scan(&plainCount)
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2`, tenantID, audited.Data.ID).Scan(&auditedCount)
	if plainCount != 0 {
		t.Fatalf("expected no audit row for unaudited entity, got %d", plainCount)
	}
	if auditedCount != 1 {
		t.Fatalf("expected exactly 1 audit row for audited entity, got %d", auditedCount)
	}

	events, err := crud.ListAuditEvents(ctx, "test.audited_orders", audited.Data.ID, rc)
	if err != nil || !events.IsOk {
		t.Fatalf("list audit events failed: %v %+v", err, events)
	}
	if len(events.Data) != 1 || events.Data[0].Action != "create" {
		t.Fatalf("got %+v", events.Data)
	}
}
