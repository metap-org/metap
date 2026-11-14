package graphqlgateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/graphql-go/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// fakeBackend serves records from memory and records the last inputs it was called with.
type fakeBackend struct {
	records         map[uuid.UUID]crud.RecordDto
	lastList        query.ListInput
	lastVersion     int32
	lastAggregate   query.AggregateSpec
	getCalls        int
	aggregateResult []json.RawMessage
}

func (f *fakeBackend) List(_ context.Context, entity string, input query.ListInput, _ *permission.RequestContext) (crud.ListResult, error) {
	f.lastList = input
	var out []crud.RecordDto
	for _, r := range f.records {
		if r.Entity == entity {
			out = append(out, r)
		}
	}
	next := "cursor-2"
	return crud.ListResult{Data: out, Page: crud.PageInfo{Limit: input.Limit, NextCursor: &next}}, nil
}

func (f *fakeBackend) Get(_ context.Context, _ string, id uuid.UUID, _ *permission.RequestContext) (crud.GetResult, error) {
	f.getCalls++
	r, ok := f.records[id]
	if !ok {
		return crud.GetResult{}, crud.NewServiceErrorMsg(404, "not_found", "not found")
	}
	return crud.GetResult{Record: r, Capabilities: crud.RecordCapabilities{CanUpdate: true, WritableFields: []string{"enabled"}}}, nil
}

func (f *fakeBackend) GetMany(context.Context, string, []uuid.UUID, *permission.RequestContext) ([]crud.GetManyResult, error) {
	return []crud.GetManyResult{}, nil
}

func (f *fakeBackend) Create(_ context.Context, entity string, data crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	r := crud.RecordDto{ID: uuid.New(), Entity: entity, Data: data, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.records[r.ID] = r
	return r, nil
}

func (f *fakeBackend) Update(_ context.Context, _ string, id uuid.UUID, v int32, _ crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	f.lastVersion = v
	return f.records[id], nil
}

func (f *fakeBackend) Transition(_ context.Context, _ string, id uuid.UUID, _ string, v int32, _ crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	f.lastVersion = v
	return f.records[id], nil
}

func (f *fakeBackend) Delete(_ context.Context, _ string, id uuid.UUID, v int32, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	f.lastVersion = v
	return f.records[id], nil
}

func (f *fakeBackend) Aggregate(_ context.Context, _ string, spec query.AggregateSpec, _ *permission.RequestContext) ([]json.RawMessage, error) {
	f.lastAggregate = spec
	return f.aggregateResult, nil
}

func raw(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// shopRegistry: customers (with a workflow) <- orders (a Reference to a customer), plus three plain
// entities, enough to exercise every generated field kind without depending on any product.
func shopRegistry(t *testing.T) *metadata.MetadataRegistry {
	t.Helper()
	table := func(name string) string { return "shop." + name[len("shop."):] }
	plain := func(name string, fields ...metadata.EntityField) metadata.EntityDefinition {
		return metadata.EntityDefinition{Name: name, Label: name, TableName: table(name), Fields: fields,
			ListViews: []metadata.EntityListView{{Name: "default", Label: "Default", Fields: []string{fields[0].Name}, MaxLimit: 50}}}
	}
	name := metadata.NewField("name", "Name", metadata.KindString).Require()
	customers := plain("shop.customers", name, metadata.EnumField("status", "Status", "active", "archived").Require())
	customers.Workflow = &metadata.EntityWorkflow{
		StateField: "status", InitialState: "active", TerminalStates: []string{"archived"},
		Transitions: []metadata.WorkflowTransition{metadata.Transition("archive", "active", "archived", "Archive")},
	}
	orders := plain("shop.orders",
		metadata.RefField("customerId", "Customer", "shop.customers", "name").Require().Index(),
		metadata.NewField("enabled", "Enabled", metadata.KindBoolean),
		metadata.NewField("total", "Total", metadata.KindNumber))
	registry := metadata.NewMetadataRegistry()
	for _, e := range []metadata.EntityDefinition{customers, orders, plain("shop.regions", name), plain("shop.items", name), plain("shop.tags", name)} {
		require.NoError(t, registry.Register(e))
	}
	return registry
}

func newTestSchema(t *testing.T) (graphql.Schema, *fakeBackend, uuid.UUID, uuid.UUID) {
	t.Helper()
	registry := shopRegistry(t)

	customerID, policyID := uuid.New(), uuid.New()
	status := "active"
	backend := &fakeBackend{records: map[uuid.UUID]crud.RecordDto{
		customerID: {ID: customerID, Entity: "shop.customers", Status: &status, Version: 3,
			Data: crud.JsonObject{"name": raw("shop.example.com")}},
		policyID: {ID: policyID, Entity: "shop.orders", Version: 2,
			Data: crud.JsonObject{"customerId": raw(customerID.String()), "enabled": raw(true), "total": raw(1200)}},
	}}
	backends := map[string]crud.RecordBackend{}
	for _, name := range []string{"shop.regions", "shop.customers", "shop.orders", "shop.items", "shop.tags"} {
		backends[name] = backend
	}
	schema, err := BuildSchema(registry, backends)
	require.NoError(t, err)
	return schema, backend, customerID, policyID
}

func run(t *testing.T, schema graphql.Schema, q string, vars map[string]interface{}) map[string]interface{} {
	t.Helper()
	ctx := withRequestContext(context.Background(), &permission.RequestContext{})
	result := graphql.Do(graphql.Params{Schema: schema, RequestString: q, VariableValues: vars, Context: ctx})
	require.Empty(t, result.Errors, "query errors")
	b, _ := json.Marshal(result.Data)
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// The list query shape @metap/platform-ui's useGraphQLRecords sends: a reference is a flat id
// ("customerId") plus, when the field has a refDisplayField, its relation field ("customer { id name }").
func TestPlatformUIListQueryShape(t *testing.T) {
	schema, backend, customerID, _ := newTestSchema(t)
	out := run(t, schema, `query List($filter: Json, $sort: String, $jql: String, $limit: Int) {
		result: shopOrdersList(filter: $filter, sort: $sort, jql: $jql, limit: $limit) {
			records { id entity code status version createdAt updatedAt capabilities
				customerId customer { id name } enabled total }
			nextCursor hasMore
		}
	}`, map[string]interface{}{"filter": map[string]interface{}{"customerId": customerID.String()}, "jql": "enabled = true", "limit": 20})

	result := out["result"].(map[string]interface{})
	assert.Equal(t, "cursor-2", result["nextCursor"])
	assert.Equal(t, true, result["hasMore"])
	records := result["records"].([]interface{})
	require.Len(t, records, 1)
	rec := records[0].(map[string]interface{})
	assert.Equal(t, true, rec["enabled"])
	assert.Equal(t, float64(1200), rec["total"])
	assert.Equal(t, customerID.String(), rec["customerId"])
	assert.Equal(t, map[string]interface{}{"id": customerID.String(), "name": "shop.example.com"}, rec["customer"])

	require.NotNil(t, backend.lastList.JQL)
	assert.Equal(t, "enabled = true", *backend.lastList.JQL)
	assert.Equal(t, int64(20), backend.lastList.Limit)
	assert.Equal(t, [][2]string{{"customerId", customerID.String()}}, backend.lastList.Filters)
}

func TestGetReturnsCapabilitiesAndMemoizesReferences(t *testing.T) {
	schema, backend, customerID, policyID := newTestSchema(t)
	out := run(t, schema, `query Get($id: ID!) {
		result: shopOrders(id: $id) { id version capabilities customerId customer { id name status } }
		again: shopOrders(id: $id) { customer { id } }
	}`, map[string]interface{}{"id": policyID.String()})

	result := out["result"].(map[string]interface{})
	assert.Equal(t, float64(2), result["version"])
	assert.Equal(t, true, result["capabilities"].(map[string]interface{})["canUpdate"])
	assert.Equal(t, customerID.String(), result["customerId"])
	assert.Equal(t, "active", result["customer"].(map[string]interface{})["status"])
	assert.Equal(t, customerID.String(), out["again"].(map[string]interface{})["customer"].(map[string]interface{})["id"])
	// 2 policy gets + 1 zone get: the second zone resolution hits the per-request cache.
	assert.Equal(t, 3, backend.getCalls)
}

func TestMutationsTakeExpectedVersion(t *testing.T) {
	schema, backend, customerID, policyID := newTestSchema(t)
	out := run(t, schema, `mutation Create($data: Json!) {
		result: createShopOrders(data: $data) { id enabled }
	}`, map[string]interface{}{"data": map[string]interface{}{"customerId": customerID.String(), "enabled": false}})
	assert.Equal(t, false, out["result"].(map[string]interface{})["enabled"])

	run(t, schema, `mutation Update($id: ID!, $expectedVersion: Int!, $data: Json!) {
		result: updateShopOrders(id: $id, expectedVersion: $expectedVersion, data: $data) { id }
	}`, map[string]interface{}{"id": policyID.String(), "expectedVersion": 2, "data": map[string]interface{}{"enabled": true}})
	assert.Equal(t, int32(2), backend.lastVersion)

	run(t, schema, `mutation Delete($id: ID!, $expectedVersion: Int!) {
		result: deleteShopOrders(id: $id, expectedVersion: $expectedVersion) { id }
	}`, map[string]interface{}{"id": policyID.String(), "expectedVersion": 7})
	assert.Equal(t, int32(7), backend.lastVersion)

	run(t, schema, `mutation Transition($id: ID!, $action: String!, $expectedVersion: Int!, $data: Json) {
		result: transitionShopCustomers(id: $id, action: $action, expectedVersion: $expectedVersion, data: $data) { id status }
	}`, map[string]interface{}{"id": customerID.String(), "action": "archive", "expectedVersion": 3, "data": nil})
	assert.Equal(t, int32(3), backend.lastVersion)
}

func TestAggregateEnvelope(t *testing.T) {
	schema, backend, _, _ := newTestSchema(t)
	backend.aggregateResult = []json.RawMessage{raw(map[string]interface{}{"group": "block", "count": 4})}
	out := run(t, schema, `query Aggregate($entity: String!, $spec: Json!) {
		result: aggregate(entity: $entity, spec: $spec)
	}`, map[string]interface{}{"entity": "shop.items", "spec": map[string]interface{}{
		"metrics": []interface{}{"count"}, "groupBy": "action", "filters": map[string]interface{}{"customerId": "z"},
	}})
	assert.Equal(t, map[string]interface{}{"data": []interface{}{map[string]interface{}{"group": "block", "count": float64(4)}}}, out["result"])
	require.NotNil(t, backend.lastAggregate.GroupBy)
	assert.Equal(t, "action", *backend.lastAggregate.GroupBy)
	assert.Equal(t, map[string]string{"customerId": "z"}, backend.lastAggregate.Filters)
}

// control-plane/dataplane's list query: a filter + plain scalar selection.
func TestControlPlaneListShape(t *testing.T) {
	schema, _, customerID, _ := newTestSchema(t)
	out := run(t, schema, `query($filter: Json, $limit: Int) {
		result: shopOrdersList(filter: $filter, limit: $limit) { records { id status enabled total } }
	}`, map[string]interface{}{"filter": map[string]interface{}{"customerId": customerID.String()}, "limit": 200})
	assert.Len(t, out["result"].(map[string]interface{})["records"], 1)
}

func TestListLimitBounds(t *testing.T) {
	_, err := listInputFromArgs(map[string]interface{}{"limit": 201})
	assert.Error(t, err)
	in, err := listInputFromArgs(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, int64(30), in.Limit)
}

func TestRelationFieldName(t *testing.T) {
	taken := map[string]bool{"entity": true, "owner": true, "status": true}
	assert.Equal(t, "customer", RelationFieldName("customerId", taken))
	assert.Equal(t, "entityRef", RelationFieldName("entity", taken), "no Id suffix")
	assert.Equal(t, "entityIdRef", RelationFieldName("entityId", taken), "stripped name collides with the envelope")
	assert.Equal(t, "ownerIdRef", RelationFieldName("ownerId", taken), "stripped name collides with a declared field")
	assert.Equal(t, "IdRef", RelationFieldName("Id", taken))
}

// RelatedRecordsPanel and control-plane select reference fields bare — valid now.
func TestBareReferenceSelection(t *testing.T) {
	schema, _, customerID, _ := newTestSchema(t)
	out := run(t, schema, `{ result: shopOrdersList { records { id customerId } } }`, nil)
	rec := out["result"].(map[string]interface{})["records"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, customerID.String(), rec["customerId"])
}

func TestLimits(t *testing.T) {
	l := Limits{MaxDepth: 4, MaxComplexity: 6}
	assert.NoError(t, checkLimits(`{ a { b { c { d } } } }`, "", l))
	assert.ErrorContains(t, checkLimits(`{ a { b { c { d { e } } } } }`, "", l), "depth 5")
	assert.ErrorContains(t, checkLimits(`{ x1: a x2: a x3: a x4: a x5: a x6: a x7: a }`, "", l), "7 field selections")
	// Fragments count where they are spread, and a fragment cycle doesn't loop forever.
	assert.ErrorContains(t, checkLimits(`{ a { ...F } } fragment F on T { b { c { d { e } } } }`, "", l), "depth 5")
	assert.NoError(t, checkLimits(`{ ...F } fragment F on Query { a ...F }`, "", l))
	// Only the selected operation is checked; unparseable input is left to graphql.Do.
	assert.NoError(t, checkLimits(`query Ok { a } query Deep { a { b { c { d { e } } } } }`, "Ok", l))
	assert.NoError(t, checkLimits(`{ unterminated`, "", l))
}

func TestReadOnlyOnlySchemaHasNoMutationType(t *testing.T) {
	registry := metadata.NewMetadataRegistry()
	ro := metadata.EntityDefinition{Name: "shop.events", Label: "e", TableName: "shop.events",
		Fields:    []metadata.EntityField{metadata.NewField("name", "Name", metadata.KindString)},
		ListViews: []metadata.EntityListView{{Name: "default", Label: "d", Fields: []string{"name"}, MaxLimit: 10}}}
	require.NoError(t, registry.Register(ro))
	schema, err := BuildSchema(registry, map[string]crud.RecordBackend{"shop.events": readOnlyFake{&fakeBackend{}}})
	require.NoError(t, err)
	assert.Nil(t, schema.MutationType())
}

type readOnlyFake struct{ *fakeBackend }

func (readOnlyFake) ReadOnly() bool { return true }
