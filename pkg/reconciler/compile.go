// Compile(desired) -> PhysicalSchema — the desired-state half of the reconcile pipeline. A pure
// function: same EntityDefinition always compiles to the same PhysicalSchema, no I/O.
package reconciler

import (
	"fmt"
	"strings"

	"github.com/metap-org/metap/pkg/metadata"
)

// FrameworkColumn is one of the fixed columns every per-entity table has, independent of
// EntityFields.
type frameworkColumn struct {
	name     string
	sqlType  string
	nullable bool
}

var frameworkColumns = []frameworkColumn{
	{"id", "uuid", false},
	{"tenant_id", "uuid", false},
	{"code", "varchar(120)", true},
	{"status", "varchar(80)", true},
	{"version", "integer", false},
	{"deleted", "boolean", false},
	{"created_at", "timestamptz", false},
	{"updated_at", "timestamptz", false},
	{"created_by", "uuid", true},
	{"updated_by", "uuid", true},
}

// legacyDataColumn is the pre-Phase-106 payload column. It is no longer part of any desired
// schema; Introspect still recognises it on a table that has not been migrated yet.
const legacyDataColumn = "data"

// FrameworkColumnNames returns every framework column's name, in declaration order.
func FrameworkColumnNames() []string {
	names := make([]string, len(frameworkColumns))
	for i, c := range frameworkColumns {
		names[i] = c.name
	}
	return names
}

// EntitySchema: every table-per-entity table lives in this schema, never "public" — public is
// where the shared users/policies/... framework tables live.
const EntitySchema = "entities"

// TableNameFor mangles an EntityDefinition.Name's dotted namespace ("hr.employees") into a bare
// SQL identifier ("hr_employees") — used for index/trigger/function naming, where a schema
// prefix would just be noise.
func TableNameFor(entityName string) string {
	return strings.ReplaceAll(entityName, ".", "_")
}

// QualifiedTableNameFor is the actual physical table identifier EntitySchema + TableNameFor's
// mangled name uses, e.g. "entities.jira_issues".
func QualifiedTableNameFor(entityName string) string {
	return QualifiedTableNameIn(entityName, EntitySchema)
}

// QualifiedTableNameIn is the same as QualifiedTableNameFor, but for a caller that already knows
// which schema it wants instead of always taking the one shared EntitySchema.
func QualifiedTableNameIn(entityName, schema string) string {
	return schema + "." + TableNameFor(entityName)
}

// schemaOf is the schema portion of an already-qualified table identifier ("waf.zones" ->
// "waf"). Panics on a tableName with no "." at all — Compile (this function's only caller) is
// never invoked for a generic-table entity.
func schemaOf(tableName string) string {
	schema, _, ok := strings.Cut(tableName, ".")
	if !ok {
		panic(fmt.Sprintf("reconciler: Compile called with a non-schema-qualified table_name: %q", tableName))
	}
	return schema
}

// CheckTableNameLength rejects an entity name whose mangled table name would exceed Postgres's
// 63-byte identifier limit — Postgres silently truncates rather than erroring, which could let
// two entity names collide on one physical table with no warning.
func CheckTableNameLength(entityName string) error {
	mangled := TableNameFor(entityName)
	if len(mangled) > 63 {
		return fmt.Errorf(
			"entity name '%s' mangles to a %d-byte table name ('%s'), over Postgres's 63-byte identifier limit — choose a shorter entity name",
			entityName, len(mangled), mangled)
	}
	return nil
}

func indexName(entityName, fieldName, kind string) string {
	return kind + "_" + TableNameFor(entityName) + "_" + fieldName
}

func fnv1a(data []byte) uint32 {
	var hash uint32 = 0x811c9dc5
	for _, b := range data {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return hash
}

// compositeUniqueIndexName is the deterministic name for a composite UniqueConstraints entry —
// the same uniq_<table>_<field> shape a single Unique:true field gets, extended to join every
// field in the group with "_". Truncates deterministically with a short hash suffix rather than
// rejecting the entity outright when the name would exceed 63 bytes.
func compositeUniqueIndexName(entityName string, fields []string) string {
	full := "uniq_" + TableNameFor(entityName) + "_" + strings.Join(fields, "_")
	if len(full) <= 63 {
		return full
	}
	suffix := fmt.Sprintf("_%08x", fnv1a([]byte(full)))
	keep := 63 - len(suffix)
	end := keep
	if end > len(full) {
		end = len(full)
	}
	for end > 0 && !utf8RuneBoundary(full, end) {
		end--
	}
	return full[:end] + suffix
}

// utf8RuneBoundary reports whether byte index i in s falls on a UTF-8 rune boundary.
func utf8RuneBoundary(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	// A continuation byte has its top two bits set to 10 — not a boundary.
	return s[i]&0xC0 != 0x80
}

// fieldIndexExpression is the expression a field contributes to an index — its own column,
// whether it is that field's single-field index or one of several fields inside a composite
// UniqueConstraints entry.
func fieldIndexExpression(field metadata.EntityField) string {
	return fmt.Sprintf(`"%s"`, field.Name)
}

// Compile compiles an EntityDefinition into its target PhysicalSchema under table-per-entity.
//
//   - Every field (except code/status, which are framework columns) gets its own typed,
//     nullable column; there is no data jsonb payload (Phase 106).
//   - Every Reference field with a RefEntity gets a real FK (referential integrity).
//   - Indexed/Sortable/Unique fields get a btree index on the column (Unique: a partial unique
//     index WHERE deleted = false); Searchable string/enum fields a GIN index (tsvector for
//     SearchMode=="fts", trigram otherwise).
func Compile(entity metadata.EntityDefinition) (PhysicalSchema, error) {
	if err := CheckTableNameLength(entity.Name); err != nil {
		return PhysicalSchema{}, err
	}
	// Uses entity.TableName as-is, not a freshly recomputed QualifiedTableNameFor(entity.Name) —
	// a caller may have called QualifiedTableNameIn with a different schema, and compile() must
	// manage DDL against the *actual* table CrudService reads/writes, not a recomputed guess.
	entitySchema := schemaOf(entity.TableName)
	schema := EmptySchema(entity.TableName)

	frameworkNames := map[string]bool{}
	for _, c := range frameworkColumns {
		frameworkNames[c.name] = true
		schema.Columns[c.name] = ColumnSpec{SQLType: c.sqlType, Nullable: c.nullable, Origin: ColumnOrigin{Kind: OriginFramework}}
	}

	// Every table-per-entity table is shared by every tenant routed to it, and every CrudService/
	// QueryPlanner query filters tenant_id = $n — without an index leading with tenant_id, a
	// tenant's list/get on a large shared table is a sequential scan of every tenant's rows.
	schema.Indexes[indexName(entity.Name, "tenant_id", "idx")] = IndexSpec{Expression: tenantColumnExpr, Valid: true}

	for _, field := range entity.Fields {
		// "code"/"status" are the two framework columns an EntityField is *expected* to
		// name-collide with — CrudService already mirrors a code-holding or workflow-state
		// field's value into these directly, independent of this compiler's own machinery.
		if field.Name != "code" && field.Name != "status" && (frameworkNames[field.Name] || field.Name == legacyDataColumn) {
			return PhysicalSchema{}, fmt.Errorf("entity '%s' field '%s' collides with a framework column name", entity.Name, field.Name)
		}
		if field.Name == "code" || field.Name == "status" {
			continue
		}

		sqlType := metadata.FieldKindSQLType(field.Kind)
		col := fmt.Sprintf(`"%s"`, field.Name)
		// Every field has its own typed, nullable column (Phase 106). Nullable on purpose: a
		// NOT NULL column cannot be added to a table that already has rows without a default,
		// and CrudService's payload validation is the required-field gate.
		schema.Columns[field.Name] = ColumnSpec{SQLType: sqlType, Nullable: true, Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}

		unique := boolVal(field.Unique)
		uniqueTenantScoped := unique && metadata.ResolveUniqueScope(field.UniqueScope) == metadata.UniqueScopeTenant
		isFkReference := field.Kind == metadata.KindReference && field.RefEntity != nil

		// Searchable: a GIN index straight over the column. Trigram and tsvector both operate on
		// text, so a searchable field must be a string-like kind. A field can also be Unique
		// (waf.zones.hostname), so this does not skip the rest of the loop body.
		if boolVal(field.Searchable) {
			if field.Kind != metadata.KindString && field.Kind != metadata.KindEnum {
				return PhysicalSchema{}, fmt.Errorf("entity '%s' field '%s' is searchable but its kind is not string or enum", entity.Name, field.Name)
			}
			fts := field.SearchMode != nil && *field.SearchMode == "fts"
			var kind, expr string
			if fts {
				kind, expr = "gin", fmt.Sprintf("to_tsvector('simple', %s)", col)
			} else {
				kind, expr = "trgm", fmt.Sprintf("%s gin_trgm_ops", col)
			}
			schema.Indexes[indexName(entity.Name, field.Name, kind)] = IndexSpec{Expression: expr, Using: "gin", Valid: true}
		}

		indexed := unique || boolVal(field.Indexed) || boolVal(field.Sortable) || isFkReference
		if indexed {
			kind := "idx"
			if unique {
				kind = "uniq"
			}
			// A non-unique FK column keeps a single-column index: Postgres's own FK check on the
			// referenced row (ON DELETE RESTRICT) looks the column up by itself.
			leadWithTenant := uniqueTenantScoped || (!unique && !isFkReference)
			idx := IndexSpec{Expression: withTenantPrefix(col, leadWithTenant), Unique: unique, Valid: true}
			// A Unique field gets a *partial* unique index (WHERE deleted = false), never a
			// blanket table UNIQUE constraint.
			if unique {
				idx.WhereClause = "deleted = false"
			}
			schema.Indexes[indexName(entity.Name, field.Name, kind)] = idx
		}
		if isFkReference {
			schema.ForeignKeys[fmt.Sprintf("fk_%s_%s", TableNameFor(entity.Name), field.Name)] = FkSpec{
				Column: field.Name,
				// Assumes the referenced entity lives in this *same* schema as the entity being
				// compiled — true for every Reference in this codebase today.
				RefTable:  QualifiedTableNameIn(*field.RefEntity, entitySchema),
				RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true,
			}
		}
	}

	// Composite UniqueConstraints — same partial (WHERE deleted = false) shape a single-field
	// Unique:true field gets, over all of the constraint's fields' expressions joined.
	if len(entity.UniqueConstraints) > 0 {
		fieldByName := map[string]metadata.EntityField{}
		for _, f := range entity.Fields {
			fieldByName[f.Name] = f
		}
		for _, constraint := range entity.UniqueConstraints {
			exprs := make([]string, len(constraint.Fields))
			for i, name := range constraint.Fields {
				exprs[i] = fieldIndexExpression(fieldByName[name])
			}
			tenantScoped := metadata.ResolveUniqueScope(constraint.Scope) == metadata.UniqueScopeTenant
			schema.Indexes[compositeUniqueIndexName(entity.Name, constraint.Fields)] = IndexSpec{
				Expression: withTenantPrefix(strings.Join(exprs, ", "), tenantScoped), Unique: true, Valid: true, WhereClause: "deleted = false",
			}
		}
	}

	return schema, nil
}

func boolVal(p *bool) bool { return p != nil && *p }

// tenantColumnExpr is the tenant_id column as an index expression.
const tenantColumnExpr = `"tenant_id"`

// withTenantPrefix prepends tenant_id to an index expression so the index serves the
// tenant_id = $n predicate every query carries (and, for a unique index, makes uniqueness
// per tenant rather than across every tenant sharing the table).
func withTenantPrefix(expr string, prefix bool) string {
	if !prefix {
		return expr
	}
	return tenantColumnExpr + ", " + expr
}
