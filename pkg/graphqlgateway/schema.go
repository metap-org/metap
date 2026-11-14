// Package graphqlgateway is a generic GraphQL BFF: a forwarding gateway in front of any set of
// services' RecordBackends (normally GrpcBackends, one per upstream service). It builds the schema
// at runtime from a MetadataRegistry. A product adds its own non-CRUD fields through WithExtensions
// and keeps its own main; nothing here knows an entity name. Concretely, per entity in the metadata
// registry:
//   - an object type named after the entity ("shop.orders" -> ShopOrders) with the
//     record envelope (id, entity, code, status, version, createdAt, updatedAt, capabilities)
//     plus every declared field flattened to the top level, typed by kind. A reference field is
//     a plain ID ("customerId"), the same value create/update take; its target record is a separate
//     relation field ("customer { name }", RelationFieldName), resolved through the target
//     entity's own backend. (Reads match writes, and a bare "customerId" selection — what
//     control-plane and RelatedRecordsPanel send — is valid.)
//   - a Connection type ({ records, nextCursor, hasMore });
//   - query fields shopOrders(id) / shopOrdersList(filter, sort, cursor, limit,
//     listView, jql), plus one shared aggregate(entity, spec): Json!;
//   - mutations create/update/delete (+ transition for an entity with a workflow), taking
//     expectedVersion and an optional reason.
package graphqlgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/graphql-go/graphql"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

type contextKey int

const (
	requestContextKey contextKey = iota
	recordCacheKey
)

func withRequestContext(ctx context.Context, rc *permission.RequestContext) context.Context {
	ctx = context.WithValue(ctx, requestContextKey, rc)
	return context.WithValue(ctx, recordCacheKey, &recordCache{entries: map[string]*recordHandle{}})
}

func requestContextFrom(ctx context.Context) *permission.RequestContext {
	rc, _ := ctx.Value(requestContextKey).(*permission.RequestContext)
	if rc == nil {
		// Unreachable in practice — the HTTP handler always sets this before calling
		// graphql.Do — but a resolver must never nil-deref on a programming slip.
		return &permission.RequestContext{}
	}
	return rc
}

// recordCache memoizes reference lookups for one request, so a list of 50 rules all pointing at the
// same zone costs one upstream Get, not 50.
type recordCache struct {
	mu      sync.Mutex
	entries map[string]*recordHandle // nil value = looked up, not found/denied
}

// recordHandle is the source value of every entity object: the envelope plus the decoded data
// bag that the flattened field resolvers read from.
type recordHandle struct {
	dto          crud.RecordDto
	data         map[string]interface{}
	capabilities *crud.RecordCapabilities
}

func newRecordHandle(dto crud.RecordDto, capabilities *crud.RecordCapabilities) (*recordHandle, error) {
	data, err := jsonObjectToMap(dto.Data)
	if err != nil {
		return nil, err
	}
	return &recordHandle{dto: dto, data: data, capabilities: capabilities}, nil
}

func (h *recordHandle) envelope(key string) interface{} {
	switch key {
	case "id":
		return h.dto.ID.String()
	case "entity":
		return h.dto.Entity
	case "code":
		if h.dto.Code == nil {
			return nil
		}
		return *h.dto.Code
	case "status":
		if h.dto.Status == nil {
			return nil
		}
		return *h.dto.Status
	case "version":
		return h.dto.Version
	case "createdAt":
		return h.dto.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00")
	case "updatedAt":
		return h.dto.UpdatedAt.Format("2006-01-02T15:04:05.000Z07:00")
	case "capabilities":
		if h.capabilities == nil {
			return nil
		}
		return capabilitiesToMap(*h.capabilities)
	}
	return nil
}

func capabilitiesToMap(c crud.RecordCapabilities) interface{} {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var out interface{} // nosemgrep: go.lang.security.deserialization.unsafe-deserialization-interface.go-unsafe-deserialization-interface
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

type connectionHandle struct {
	records    []*recordHandle
	nextCursor interface{}
	hasMore    bool
}

var envelopeFieldNames = map[string]bool{
	"id": true, "entity": true, "code": true, "status": true,
	"version": true, "createdAt": true, "updatedAt": true, "capabilities": true,
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// typeName: "shop.orders" -> "ShopOrders".
func typeName(entityName string) string {
	parts := strings.FieldsFunc(entityName, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	for i, p := range parts {
		parts[i] = capitalize(p)
	}
	return strings.Join(parts, "")
}

// fieldBase: "shop.orders" -> "shopOrders".
func fieldBase(entityName string) string {
	t := typeName(entityName)
	if t == "" {
		return t
	}
	return strings.ToLower(t[:1]) + t[1:]
}

func jsonObjectToMap(obj crud.JsonObject) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(obj))
	for k, raw := range obj {
		// False positive below: jsonObjectToMap converts a crud.JsonObject (map[string]json.RawMessage)
		// into the untyped map[string]interface{} shape this no-codegen GraphQL gateway's resolvers
		// need — the field set is metadata-driven at runtime, never known at compile time, so there
		// is no single concrete struct to deserialize into (same reasoning as custom.go's
		// forwardCustomMutation). Semgrep anchors a multi-line match's suppression to its first line.
		var v interface{} // nosemgrep: go.lang.security.deserialization.unsafe-deserialization-interface.go-unsafe-deserialization-interface
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func mapToJsonObject(m map[string]interface{}) (crud.JsonObject, error) {
	out := make(crud.JsonObject, len(m))
	for k, v := range m {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[k] = raw
	}
	return out, nil
}

func parseID(args map[string]interface{}, name string) (uuid.UUID, error) {
	s, _ := args[name].(string)
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%s is not a valid id: %w", name, err)
	}
	return id, nil
}

func int32Arg(args map[string]interface{}, name string) int32 {
	switch v := args[name].(type) {
	case int:
		return int32(v)
	case int32:
		return v
	case int64:
		return int32(v)
	case float64:
		return int32(v)
	default:
		return 0
	}
}

func stringArg(args map[string]interface{}, name string) *string {
	if s, ok := args[name].(string); ok {
		return &s
	}
	return nil
}

func objectArg(args map[string]interface{}, name string) (crud.JsonObject, bool, error) {
	m, ok := args[name].(map[string]interface{})
	if !ok {
		return nil, false, nil
	}
	obj, err := mapToJsonObject(m)
	return obj, true, err
}

// schemaBuilder holds what every resolver needs: which backend owns each entity, and the object
// type built for each entity (a reference field's type is the target entity's object, so types
// are created first and their fields filled lazily via FieldsThunk).
type schemaBuilder struct {
	registry *metadata.MetadataRegistry
	backends map[string]crud.RecordBackend
	types    map[string]*graphql.Object
}

// ReadOnlyBackend marks a backend whose entity gets queries but no create/update/delete
// mutations in the schema.
type ReadOnlyBackend interface{ ReadOnly() bool }

func (b *schemaBuilder) backendFor(entity string) (crud.RecordBackend, error) {
	backend, ok := b.backends[entity]
	if !ok {
		return nil, fmt.Errorf("unknown entity %q", entity)
	}
	return backend, nil
}

// loadReference resolves one reference field's target record, memoized per request. A denied or
// missing target resolves to null rather than failing the whole parent record.
func (b *schemaBuilder) loadReference(ctx context.Context, entity string, id uuid.UUID) (*recordHandle, error) {
	cache, _ := ctx.Value(recordCacheKey).(*recordCache)
	key := entity + "/" + id.String()
	if cache != nil {
		cache.mu.Lock()
		h, hit := cache.entries[key]
		cache.mu.Unlock()
		if hit {
			return h, nil
		}
	}
	backend, err := b.backendFor(entity)
	if err != nil {
		return nil, err
	}
	result, err := backend.Get(ctx, entity, id, requestContextFrom(ctx))
	if err != nil {
		// A reference the caller cannot read (denied or gone) resolves to null; anything else
		// is a real failure.
		if _, business := crud.AsServiceError(err); !business {
			return nil, err
		}
		result = crud.GetResult{}
		err = nil
	}
	var h *recordHandle
	capabilities := result.Capabilities
	h, err = newRecordHandle(result.Record, &capabilities)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache.mu.Lock()
		cache.entries[key] = h
		cache.mu.Unlock()
	}
	return h, nil
}

func envelopeResolver(key string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		h, ok := p.Source.(*recordHandle)
		if !ok {
			return nil, nil
		}
		return h.envelope(key), nil
	}
}

func dataResolver(fieldName string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		h, ok := p.Source.(*recordHandle)
		if !ok {
			return nil, nil
		}
		return h.data[fieldName], nil
	}
}

// RelationFieldName names the relation field that carries a reference field's target record:
// strip a trailing "Id" ("customerId" -> "zone", "domainId" -> "domain"); when there is no such
// suffix, or the stripped name is already taken by an envelope or declared field, append "Ref"
// instead ("owner" -> "ownerRef"). @metap/platform-ui's graphqlNaming.ts relationFieldName
// implements the same rule — keep the two in step.
func RelationFieldName(fieldName string, taken map[string]bool) string {
	if len(fieldName) > 2 && strings.HasSuffix(fieldName, "Id") {
		if base := fieldName[:len(fieldName)-2]; !taken[base] {
			return base
		}
	}
	return fieldName + "Ref"
}

func scalarFor(kind metadata.FieldKind) graphql.Output {
	switch kind {
	case metadata.KindID:
		return graphql.ID
	case metadata.KindNumber, metadata.KindMoney:
		return graphql.Float
	case metadata.KindBoolean:
		return graphql.Boolean
	case metadata.KindJSON:
		return jsonScalar
	default: // string, enum, date, datetime
		return graphql.String
	}
}

func (b *schemaBuilder) entityFields(summary metadata.EntitySummary) graphql.Fields {
	fields := graphql.Fields{}
	for _, key := range []string{"id", "entity", "code", "status", "createdAt", "updatedAt"} {
		fields[key] = &graphql.Field{Type: graphql.String, Resolve: envelopeResolver(key)}
	}
	fields["version"] = &graphql.Field{Type: graphql.Int, Resolve: envelopeResolver("version")}
	fields["capabilities"] = &graphql.Field{Type: jsonScalar, Resolve: envelopeResolver("capabilities")}

	taken := map[string]bool{}
	for k := range envelopeFieldNames {
		taken[k] = true
	}
	for _, f := range summary.Fields {
		taken[f.Name] = true
	}

	for _, f := range summary.Fields {
		if envelopeFieldNames[f.Name] {
			continue
		}
		name := f.Name
		if f.Kind == metadata.KindReference {
			// Flat: the reference field itself is the plain id — the same value create/update
			// take in `data`, so a record reads back exactly as it is written.
			fields[name] = &graphql.Field{Type: graphql.ID, Resolve: dataResolver(name)}
			if f.RefEntity == nil {
				continue
			}
			// The referenced record, when a caller wants more than its id, is a separate
			// relation field: "customerId" -> "zone" (see RelationFieldName).
			if target, ok := b.types[*f.RefEntity]; ok {
				refEntity := *f.RefEntity
				fields[RelationFieldName(name, taken)] = &graphql.Field{
					Type: target,
					Resolve: func(p graphql.ResolveParams) (interface{}, error) {
						h, ok := p.Source.(*recordHandle)
						if !ok {
							return nil, nil
						}
						idStr, _ := h.data[name].(string)
						id, err := uuid.Parse(idStr)
						if err != nil {
							return nil, nil
						}
						ref, err := b.loadReference(p.Context, refEntity, id)
						if err != nil || ref == nil {
							return nil, err
						}
						return ref, nil
					},
				}
			}
			// A target entity this gateway doesn't serve gets no relation field — just the id.
			continue
		}
		fields[name] = &graphql.Field{Type: scalarFor(f.Kind), Resolve: dataResolver(name)}
	}
	return fields
}

func connectionType(entityName string, recordType *graphql.Object) *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: typeName(entityName) + "Connection",
		Fields: graphql.Fields{
			"records": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(recordType))),
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					return p.Source.(*connectionHandle).records, nil
				},
			},
			"nextCursor": &graphql.Field{
				Type: graphql.String,
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					return p.Source.(*connectionHandle).nextCursor, nil
				},
			},
			"hasMore": &graphql.Field{
				Type: graphql.NewNonNull(graphql.Boolean),
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					return p.Source.(*connectionHandle).hasMore, nil
				},
			},
		},
	})
}

func (b *schemaBuilder) getField(entity string, recordType *graphql.Object) *graphql.Field {
	backend := b.backends[entity]
	return &graphql.Field{
		Type: recordType,
		Args: graphql.FieldConfigArgument{
			"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			id, err := parseID(p.Args, "id")
			if err != nil {
				return nil, err
			}
			result, err := backend.Get(p.Context, entity, id, requestContextFrom(p.Context))
			if err != nil {
				return nil, gqlError(err)
			}
			capabilities := result.Capabilities
			return newRecordHandle(result.Record, &capabilities)
		},
	}
}

// listInputFromArgs mirrors metap-graphql's list_input_from_args: limit defaults to 30 and must
// be in 1..=200; filter is a flat object whose values are stringified.
func listInputFromArgs(args map[string]interface{}) (query.ListInput, error) {
	input := query.ListInput{Limit: 30}
	if _, ok := args["limit"]; ok {
		n := int64(int32Arg(args, "limit"))
		if n <= 0 || n > 200 {
			return input, fmt.Errorf("`limit` must be a positive integer no greater than 200")
		}
		input.Limit = n
	}
	input.Sort = stringArg(args, "sort")
	input.Cursor = stringArg(args, "cursor")
	input.ListView = stringArg(args, "listView")
	input.JQL = stringArg(args, "jql")
	if filter, ok := args["filter"].(map[string]interface{}); ok {
		for k, v := range filter {
			var s string
			switch tv := v.(type) {
			case string:
				s = tv
			case nil:
				continue
			default:
				raw, _ := json.Marshal(tv)
				s = string(raw)
			}
			input.Filters = append(input.Filters, [2]string{k, s})
		}
	}
	return input, nil
}

func (b *schemaBuilder) listField(entity string, connection *graphql.Object) *graphql.Field {
	backend := b.backends[entity]
	return &graphql.Field{
		Type: graphql.NewNonNull(connection),
		Args: graphql.FieldConfigArgument{
			"filter":   &graphql.ArgumentConfig{Type: jsonScalar},
			"sort":     &graphql.ArgumentConfig{Type: graphql.String},
			"cursor":   &graphql.ArgumentConfig{Type: graphql.String},
			"limit":    &graphql.ArgumentConfig{Type: graphql.Int},
			"listView": &graphql.ArgumentConfig{Type: graphql.String},
			"jql":      &graphql.ArgumentConfig{Type: graphql.String},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			input, err := listInputFromArgs(p.Args)
			if err != nil {
				return nil, err
			}
			result, err := backend.List(p.Context, entity, input, requestContextFrom(p.Context))
			if err != nil {
				return nil, gqlError(err)
			}
			conn := &connectionHandle{records: make([]*recordHandle, 0, len(result.Data))}
			for _, dto := range result.Data {
				h, err := newRecordHandle(dto, nil)
				if err != nil {
					return nil, err
				}
				conn.records = append(conn.records, h)
			}
			if result.Page.NextCursor != nil {
				conn.nextCursor = *result.Page.NextCursor
				conn.hasMore = true
			}
			return conn, nil
		},
	}
}

func (b *schemaBuilder) aggregateField() *graphql.Field {
	return &graphql.Field{
		Type: graphql.NewNonNull(jsonScalar),
		Args: graphql.FieldConfigArgument{
			"entity": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
			"spec":   &graphql.ArgumentConfig{Type: graphql.NewNonNull(jsonScalar)},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			entity, _ := p.Args["entity"].(string)
			backend, err := b.backendFor(entity)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(p.Args["spec"])
			if err != nil {
				return nil, err
			}
			var spec query.AggregateSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return nil, fmt.Errorf("invalid aggregate spec: %w", err)
			}
			result, err := backend.Aggregate(p.Context, entity, spec, requestContextFrom(p.Context))
			if err != nil {
				return nil, gqlError(err)
			}
			rows := make([]interface{}, 0, len(result))
			for _, r := range result {
				var row interface{} // nosemgrep: go.lang.security.deserialization.unsafe-deserialization-interface.go-unsafe-deserialization-interface
				if err := json.Unmarshal(r, &row); err != nil {
					return nil, err
				}
				rows = append(rows, row)
			}
			return map[string]interface{}{"data": rows}, nil
		},
	}
}

func recordResult(result crud.RecordDto, err error) (interface{}, error) {
	if err != nil {
		return nil, gqlError(err)
	}
	return newRecordHandle(result, nil)
}

func (b *schemaBuilder) addMutations(fields graphql.Fields, entity string, recordType *graphql.Object, hasWorkflow bool) {
	backend := b.backends[entity]
	pascal := typeName(entity)
	reasonArg := &graphql.ArgumentConfig{Type: graphql.String}

	fields["create"+pascal] = &graphql.Field{
		Type: recordType,
		Args: graphql.FieldConfigArgument{
			"data":   &graphql.ArgumentConfig{Type: graphql.NewNonNull(jsonScalar)},
			"reason": reasonArg,
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			data, ok, err := objectArg(p.Args, "data")
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("`data` must be an object")
			}
			return recordResult(backend.Create(p.Context, entity, data, requestContextFrom(p.Context), stringArg(p.Args, "reason")))
		},
	}

	fields["update"+pascal] = &graphql.Field{
		Type: recordType,
		Args: graphql.FieldConfigArgument{
			"id":              &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
			"expectedVersion": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
			"data":            &graphql.ArgumentConfig{Type: graphql.NewNonNull(jsonScalar)},
			"reason":          reasonArg,
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			id, err := parseID(p.Args, "id")
			if err != nil {
				return nil, err
			}
			data, ok, err := objectArg(p.Args, "data")
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("`data` must be an object")
			}
			return recordResult(backend.Update(p.Context, entity, id, int32Arg(p.Args, "expectedVersion"), data, requestContextFrom(p.Context), stringArg(p.Args, "reason")))
		},
	}

	fields["delete"+pascal] = &graphql.Field{
		Type: recordType,
		Args: graphql.FieldConfigArgument{
			"id":              &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
			"expectedVersion": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
			"reason":          reasonArg,
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			id, err := parseID(p.Args, "id")
			if err != nil {
				return nil, err
			}
			return recordResult(backend.Delete(p.Context, entity, id, int32Arg(p.Args, "expectedVersion"), requestContextFrom(p.Context), stringArg(p.Args, "reason")))
		},
	}

	if !hasWorkflow {
		return
	}
	fields["transition"+pascal] = &graphql.Field{
		Type: recordType,
		Args: graphql.FieldConfigArgument{
			"id":              &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
			"action":          &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
			"expectedVersion": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
			"data":            &graphql.ArgumentConfig{Type: jsonScalar},
			"reason":          reasonArg,
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			id, err := parseID(p.Args, "id")
			if err != nil {
				return nil, err
			}
			action, _ := p.Args["action"].(string)
			data, _, err := objectArg(p.Args, "data")
			if err != nil {
				return nil, err
			}
			return recordResult(backend.Transition(p.Context, entity, id, action, int32Arg(p.Args, "expectedVersion"), data, requestContextFrom(p.Context), stringArg(p.Args, "reason")))
		},
	}
}

// Option customizes BuildSchema.
type Option func(*buildConfig)

type buildConfig struct {
	platformBase string
	extend       func(query, mutation graphql.Fields)
}

// WithPlatformBase adds the platform admin/settings fields (users, policies, cron jobs, OAuth
// clients, dashboards, preferences, platform + tenant config), each forwarded to the matching REST
// route of the metap service at base (e.g. http://localhost:3000).
func WithPlatformBase(base string) Option { return func(c *buildConfig) { c.platformBase = base } }

// WithExtensions lets a product add its own query/mutation fields (business actions that are not
// entity CRUD). Build them with ForwardPost, IDArg, JSONScalar and RequestContextFrom.
func WithExtensions(fn func(query, mutation graphql.Fields)) Option {
	return func(c *buildConfig) { c.extend = fn }
}

// BuildSchema assembles the full schema from registry: typed CRUD fields for every entity that
// has a backend in backends (keyed by entity name), the shared aggregate query, plus whatever the
// options add.
func BuildSchema(registry *metadata.MetadataRegistry, backends map[string]crud.RecordBackend, opts ...Option) (graphql.Schema, error) {
	var cfg buildConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	b := &schemaBuilder{registry: registry, backends: backends, types: map[string]*graphql.Object{}}

	var summaries []metadata.EntitySummary
	for name := range backends {
		summary, ok := registry.GetEntityMetadata(name)
		if !ok {
			return graphql.Schema{}, fmt.Errorf("entity %q has a backend but no metadata", name)
		}
		summaries = append(summaries, summary)
	}

	// Create every object first so reference fields can point at any entity's type, then fill
	// fields lazily (FieldsThunk runs after all types exist).
	for _, summary := range summaries {
		summary := summary
		b.types[summary.Name] = graphql.NewObject(graphql.ObjectConfig{
			Name:   typeName(summary.Name),
			Fields: graphql.FieldsThunk(func() graphql.Fields { return b.entityFields(summary) }),
		})
	}

	queryFields := graphql.Fields{}
	mutationFields := graphql.Fields{}
	for _, summary := range summaries {
		recordType := b.types[summary.Name]
		base := fieldBase(summary.Name)
		queryFields[base] = b.getField(summary.Name, recordType)
		queryFields[base+"List"] = b.listField(summary.Name, connectionType(summary.Name, recordType))
		if ro, ok := b.backends[summary.Name].(ReadOnlyBackend); ok && ro.ReadOnly() {
			continue // a read model: no mutations
		}
		b.addMutations(mutationFields, summary.Name, recordType, summary.Workflow != nil)
	}
	queryFields["aggregate"] = b.aggregateField()

	if cfg.platformBase != "" {
		addPlatformFields(queryFields, mutationFields, cfg.platformBase)
	}
	if cfg.extend != nil {
		cfg.extend(queryFields, mutationFields)
	}

	queryType := graphql.NewObject(graphql.ObjectConfig{Name: "Query", Fields: queryFields})
	cfgSchema := graphql.SchemaConfig{Query: queryType}
	if len(mutationFields) > 0 { // GraphQL forbids an object type with no fields
		cfgSchema.Mutation = graphql.NewObject(graphql.ObjectConfig{Name: "Mutation", Fields: mutationFields})
	}
	return graphql.NewSchema(cfgSchema)
}
