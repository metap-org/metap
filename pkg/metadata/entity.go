// Package metadata is entity metadata — the heart of the platform: EntityDefinition/EntityField,
// MetadataCompiler (validate + hash), MetadataRegistry, the OpenAPI generator. Ported from
// metap's Rust metap-metadata crate.
package metadata

import "github.com/metap-org/metap/pkg/permission"

// FieldKind mirrors the Rust FieldKind. Reference serializes differently over REST vs. GraphQL
// (a later wave) — see the Rust doc comment for why that's intentional, not a bug.
type FieldKind string

const (
	KindID        FieldKind = "id"
	KindString    FieldKind = "string"
	KindNumber    FieldKind = "number"
	KindBoolean   FieldKind = "boolean"
	KindDate      FieldKind = "date"
	KindDatetime  FieldKind = "datetime"
	KindMoney     FieldKind = "money"
	KindEnum      FieldKind = "enum"
	KindReference FieldKind = "reference"
	KindJSON      FieldKind = "json"
)

// FieldStorage is an explicit override of the storage tier resolve_field_storage_tier would
// otherwise derive from indexed/sortable/unique/searchable.
type FieldStorage string

const (
	StorageNative FieldStorage = "native"
	StorageColumn FieldStorage = "column"
)

// EntityUniqueConstraint is a multi-field uniqueness rule — "these fields together must be
// unique", distinct from EntityField.Unique (one field, unique on its own).
type EntityUniqueConstraint struct {
	Fields []string `json:"fields"`
	// Scope: see UniqueScope. nil means UniqueScopeTenant.
	Scope *UniqueScope `json:"scope,omitempty"`
}

// UniqueScope is how far a uniqueness guarantee reaches inside one physical table. Every
// table-per-entity table is shared by every tenant routed to it (a Schema-strategy tenant on
// the default schema, or many tenants on one product schema), so "unique" has to say whether it
// means "unique among this tenant's own rows" or "unique across every tenant in the table".
//
//   - UniqueScopeTenant (the default): the partial unique index leads with tenant_id, so two
//     tenants can each have a project with key "WEB". This is what almost every business field
//     wants.
//   - UniqueScopeGlobal: the index covers the field alone, so the value is unique across every
//     tenant sharing the table — for values that identify something outside the tenant
//     (a hostname served by the edge, an API-key prefix looked up before the tenant is known).
//
// Note that "global" only reaches as far as the table: a DedicatedDb tenant has its own copy of
// the table, so platform-wide uniqueness across databases needs a registry outside the entity
// layer.
type UniqueScope string

const (
	UniqueScopeTenant UniqueScope = "tenant"
	UniqueScopeGlobal UniqueScope = "global"
)

// ResolveUniqueScope returns the effective scope of an optional scope pointer (nil → tenant).
func ResolveUniqueScope(scope *UniqueScope) UniqueScope {
	if scope == nil {
		return UniqueScopeTenant
	}
	return *scope
}

// ComputedSpec: see EntityField.Computed's doc comment. Expression is a minimal string
// template, not a general expression language.
type ComputedSpec struct {
	Expression string   `json:"expression"`
	DependsOn  []string `json:"dependsOn"`
}

// EntityField mirrors the Rust EntityField. Optional flags are pointers so a Go zero value
// (false) is distinguishable from "not set", matching the Rust Option<bool>/serde default
// behavior exactly — this matters for resolve_field_storage_tier's fallback logic.
type EntityField struct {
	Name            string        `json:"name"`
	Label           string        `json:"label"`
	Kind            FieldKind     `json:"kind"`
	Required        *bool         `json:"required,omitempty"`
	Indexed         *bool         `json:"indexed,omitempty"`
	Unique          *bool         `json:"unique,omitempty"`
	UniqueScope     *UniqueScope  `json:"uniqueScope,omitempty"`
	EnumValues      []string      `json:"enumValues,omitempty"`
	RefEntity       *string       `json:"refEntity,omitempty"`
	RefDisplayField *string       `json:"refDisplayField,omitempty"`
	Searchable      *bool         `json:"searchable,omitempty"`
	SearchMode      *string       `json:"searchMode,omitempty"`
	Sortable        *bool         `json:"sortable,omitempty"`
	Storage         *FieldStorage `json:"storage,omitempty"`
	Min             *float64      `json:"min,omitempty"`
	Max             *float64      `json:"max,omitempty"`
	MinLength       *uint32       `json:"minLength,omitempty"`
	MaxLength       *uint32       `json:"maxLength,omitempty"`
	Computed        *ComputedSpec `json:"computed,omitempty"`
	// System marks a field the platform writes, never the user: e.g. an API key's lastUsedAt,
	// a heartbeat, a counter. Create and Update reject it in a payload and Update preserves its
	// current stored value; it is written only through CrudService.SetSystemFields, which does
	// not bump version (so it never causes a version_conflict for a user editing the record),
	// does not emit an outbox event, and does not write an audit entry.
	System *bool `json:"system,omitempty"`
}

// SystemFieldNames returns the names of every System field of an entity, in declaration order.
func SystemFieldNames(entity EntityDefinition) []string {
	var names []string
	for _, f := range entity.Fields {
		if boolVal(f.System) {
			names = append(names, f.Name)
		}
	}
	return names
}

// FieldStorageTier is the storage tier a field is promoted to under table-per-entity.
type FieldStorageTier int

const (
	TierJsonb FieldStorageTier = iota
	TierGeneratedColumn
)

func boolVal(p *bool) bool { return p != nil && *p }

// ResolveFieldStorageTier derives a field's storage tier from its Storage override, falling
// back to indexed/sortable/unique/searchable when unset.
func ResolveFieldStorageTier(field EntityField) FieldStorageTier {
	if field.Storage != nil {
		if *field.Storage == StorageNative {
			return TierJsonb
		}
		return TierGeneratedColumn
	}
	if boolVal(field.Indexed) || boolVal(field.Sortable) || boolVal(field.Unique) || boolVal(field.Searchable) {
		return TierGeneratedColumn
	}
	return TierJsonb
}

// FieldHasRealColumn is always true since Phase 106 (docs/roadmap/106): every field has its own
// typed column and there is no data jsonb payload. Kept as the single question the reconciler and
// the query planner both ask, so a future exception (a virtual field) would have one place to say
// "no" instead of a second, disagreeing implementation.
func FieldHasRealColumn(field EntityField) bool {
	return true
}

// FieldKindSQLType: FieldKind -> SQL type a promoted field would use.
func FieldKindSQLType(kind FieldKind) string {
	switch kind {
	case KindString, KindEnum:
		return "text"
	case KindNumber:
		return "double precision"
	case KindMoney:
		return "numeric(18,4)"
	case KindBoolean:
		return "boolean"
	case KindDate:
		return "date"
	case KindDatetime:
		return "timestamptz"
	case KindReference, KindID:
		return "uuid"
	case KindJSON:
		return "jsonb"
	default:
		return "text"
	}
}

// EntityListView mirrors the Rust EntityListView.
type EntityListView struct {
	Name           string   `json:"name"`
	Label          string   `json:"label"`
	Fields         []string `json:"fields"`
	Filters        []string `json:"filters"`
	RequiredFields []string `json:"requiredFields,omitempty"`
	DefaultSort    *string  `json:"defaultSort,omitempty"`
	MaxLimit       uint32   `json:"maxLimit"`
}

// WorkflowTransition mirrors the Rust WorkflowTransition. Guard/Validator are
// permission.PolicyCondition — the same declarative type policies use. Besides record fields,
// a guard or validator can read the caller (`$ctx.roles`, `$ctx.userId`, any context attribute)
// and who last performed each action on the record (`$workflow.actors.<action>`), which is what
// separation of duties ("the approver is not the submitter") and per-account modes ("this
// account requires approval") are written with.
type WorkflowTransition struct {
	Action    string                            `json:"action"`
	From      string                            `json:"from"`
	To        string                            `json:"to"`
	Label     string                            `json:"label"`
	Guard     *permission.PolicyCondition       `json:"guard,omitempty"`
	Validator *permission.PolicyCondition       `json:"validator,omitempty"`
	SetFields map[string]permission.PolicyValue `json:"setFields,omitempty"`
	// GuardMessage / ValidatorMessage replace the evaluator's technical reason
	// ("condition failed: …") in the error and in capabilities, so a user is told
	// "the approver must differ from the submitter", not the condition's internals.
	GuardMessage     string `json:"guardMessage,omitempty"`
	ValidatorMessage string `json:"validatorMessage,omitempty"`
}

type EntityWorkflow struct {
	StateField     string               `json:"stateField"`
	InitialState   string               `json:"initialState"`
	TerminalStates []string             `json:"terminalStates"`
	Transitions    []WorkflowTransition `json:"transitions"`
}

// RelatedView is a declarative "show related records from another entity" section — registered
// separately via SubmitRelatedViews, not a field on EntityDefinition. See the Rust doc comment
// for why (EntityDefinition is constructed as a bare struct literal at ~50 call sites).
type RelatedView struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Entity      string   `json:"entity"`
	FilterField string   `json:"filterField"`
	Fields      []string `json:"fields"`
	Limit       *uint32  `json:"limit,omitempty"`
}

// FieldDisplayHint tells a generic renderer something about how to display a field's raw value
// beyond kind/enumValues. Registered separately via SubmitFieldDisplayHints, same reason as
// RelatedView.
type FieldDisplayHint struct {
	Field      string            `json:"field"`
	ResolveVia *string           `json:"resolveVia,omitempty"`
	EnumTones  map[string]string `json:"enumTones,omitempty"`
}

// EntityAuditConfig is an opt-in per-entity switch for the general audit trail.
type EntityAuditConfig struct {
	Enabled        bool     `json:"enabled"`
	RedactedFields []string `json:"redactedFields,omitempty"`
}

// EntityDefinition mirrors the Rust EntityDefinition.
type EntityDefinition struct {
	Name              string                   `json:"name"`
	Label             string                   `json:"label"`
	TableName         string                   `json:"tableName"`
	Fields            []EntityField            `json:"fields,omitempty"`
	ListViews         []EntityListView         `json:"listViews,omitempty"`
	Workflow          *EntityWorkflow          `json:"workflow,omitempty"`
	UniqueConstraints []EntityUniqueConstraint `json:"uniqueConstraints,omitempty"`
	Audit             *EntityAuditConfig       `json:"audit,omitempty"`
}
