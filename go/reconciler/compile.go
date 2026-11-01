// Compile(desired) -> PhysicalSchema — the desired-state half of the reconcile pipeline. A pure
// function: same EntityDefinition always compiles to the same PhysicalSchema, no I/O.
package reconciler

import (
	"fmt"
	"strings"

	"github.com/metap-org/metap-go/metadata"
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
	{"data", "jsonb", false},
	{"version", "integer", false},
	{"deleted", "boolean", false},
	{"created_at", "timestamptz", false},
	{"updated_at", "timestamptz", false},
	{"created_by", "uuid", true},
	{"updated_by", "uuid", true},
}

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

// fnv1a: not Go's hash/fnv (same risk the Rust original flags about std::hash::DefaultHasher —
// here it's the opposite concern: pinning the exact algorithm this port uses rather than
// trusting a stdlib package's implementation to never change) — implemented inline so the exact
// bytes this produces are visible and match the Rust original's fnv1a byte-for-byte, which
// matters because this name must stay stable across a toolchain upgrade for reconcile
// convergence.
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

// utf8RuneBoundary reports whether byte index i in s falls on a UTF-8 rune boundary — mirrors
// Rust's str::is_char_boundary, needed so the truncation above never splits a multi-byte
// character.
func utf8RuneBoundary(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	// A continuation byte has its top two bits set to 10 — not a boundary.
	return s[i]&0xC0 != 0x80
}

// fieldIndexExpression is the expression a field contributes to an index — same shape whether
// it's this field's own promoted single-field index or one of several fields inside a composite
// UniqueConstraints entry.
func fieldIndexExpression(field metadata.EntityField) string {
	if metadata.FieldHasRealColumn(field) {
		return fmt.Sprintf(`"%s"`, field.Name)
	}
	switch field.Kind {
	case metadata.KindDate, metadata.KindDatetime:
		return fmt.Sprintf("(data ->> '%s')", field.Name)
	default:
		return fmt.Sprintf("((data ->> '%s')::%s)", field.Name, metadata.FieldKindSQLType(field.Kind))
	}
}

// Compile compiles an EntityDefinition into its target PhysicalSchema under table-per-entity.
//
//   - Every Reference field with a RefEntity gets a real FK (referential integrity, not a
//     query-optimization decision).
//   - field.Storage == StorageColumn is the only thing that produces a real, trigger-synced
//     physical column (OriginGenerated) — every other promoted field gets an **expression
//     index** straight over data, never a new column, to avoid GENERATED ... STORED's
//     ACCESS EXCLUSIVE table rewrite by default.
//   - Searchable fields always use an expression-based GIN index (tsvector for
//     SearchMode=="fts", trigram otherwise) regardless of Storage.
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

	for _, field := range entity.Fields {
		// "code"/"status" are the two framework columns an EntityField is *expected* to
		// name-collide with — CrudService already mirrors a code-holding or workflow-state
		// field's value into these directly, independent of this compiler's own machinery.
		if field.Name != "code" && field.Name != "status" && frameworkNames[field.Name] {
			return PhysicalSchema{}, fmt.Errorf("entity '%s' field '%s' collides with a framework column name", entity.Name, field.Name)
		}
		if field.Name == "code" || field.Name == "status" {
			continue
		}

		sqlType := metadata.FieldKindSQLType(field.Kind)
		unique := boolVal(field.Unique)

		// Searchable always uses an expression-based GIN index straight over data — a field can
		// also be Unique (waf.zones.hostname, real-world case), so this doesn't unconditionally
		// skip the rest of the loop body — the two need genuinely separate index objects.
		if boolVal(field.Searchable) {
			fts := field.SearchMode != nil && *field.SearchMode == "fts"
			var kind, expr string
			if fts {
				kind, expr = "gin", fmt.Sprintf("to_tsvector('simple', (data ->> '%s'))", field.Name)
			} else {
				kind, expr = "trgm", fmt.Sprintf("(data ->> '%s') gin_trgm_ops", field.Name)
			}
			schema.Indexes[indexName(entity.Name, field.Name, kind)] = IndexSpec{Expression: expr, Using: "gin", Valid: true}
			if !unique {
				continue
			}
		}

		// A real SQL FOREIGN KEY needs a real physical column to attach to — a Reference field
		// can never stay JSONB-only once its RefEntity is set, regardless of Indexed/Storage.
		isFkReference := field.Kind == metadata.KindReference && field.RefEntity != nil
		wantsRealColumn := metadata.FieldHasRealColumn(field)

		if !wantsRealColumn && metadata.ResolveFieldStorageTier(field) == metadata.TierJsonb {
			continue
		}

		indexed := unique || boolVal(field.Indexed) || boolVal(field.Sortable) || isFkReference

		if wantsRealColumn {
			schema.Columns[field.Name] = ColumnSpec{
				SQLType: sqlType, Nullable: true,
				Origin: ColumnOrigin{Kind: OriginGenerated, SourceField: field.Name, Backfilled: true},
			}
			// A Unique field gets a *partial* unique index (WHERE deleted = false), never a
			// blanket table UNIQUE constraint.
			if indexed {
				kind := "idx"
				if unique {
					kind = "uniq"
				}
				idx := IndexSpec{Expression: fmt.Sprintf(`"%s"`, field.Name), Unique: unique, Valid: true}
				if unique {
					idx.WhereClause = "deleted = false"
				}
				schema.Indexes[indexName(entity.Name, field.Name, kind)] = idx
			}
			if isFkReference {
				schema.ForeignKeys[fmt.Sprintf("fk_%s_%s", TableNameFor(entity.Name), field.Name)] = FkSpec{
					Column: field.Name,
					// Assumes the referenced entity lives in this *same* schema as the entity
					// being compiled — true for every Reference in this codebase today.
					RefTable:  QualifiedTableNameIn(*field.RefEntity, entitySchema),
					RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true,
				}
			}
		} else {
			// Date/Datetime deliberately stay an uncast text expression — Postgres's implicit
			// text->date/text->timestamptz cast is STABLE, not IMMUTABLE, and CREATE INDEX on an
			// expression requires IMMUTABLE.
			var expr string
			switch field.Kind {
			case metadata.KindDate, metadata.KindDatetime:
				expr = fmt.Sprintf("(data ->> '%s')", field.Name)
			default:
				expr = fmt.Sprintf("((data ->> '%s')::%s)", field.Name, sqlType)
			}
			kind := "idx"
			if unique {
				kind = "uniq"
			}
			idx := IndexSpec{Expression: expr, Unique: unique, Valid: true}
			if unique {
				idx.WhereClause = "deleted = false"
			}
			schema.Indexes[indexName(entity.Name, field.Name, kind)] = idx
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
			schema.Indexes[compositeUniqueIndexName(entity.Name, constraint.Fields)] = IndexSpec{
				Expression: strings.Join(exprs, ", "), Unique: true, Valid: true, WhereClause: "deleted = false",
			}
		}
	}

	return schema, nil
}

func boolVal(p *bool) bool { return p != nil && *p }
