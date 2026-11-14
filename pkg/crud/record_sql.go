package crud

// SQL for reading and writing a record when every field has its own typed column (Phase 106,
// docs/roadmap/106-native-entity-columns-no-data-jsonb.md).
//
// The service boundary is unchanged: a record is still a JSON object (RecordDto.Data) going in and
// out. This file is the only place that translates between that object and the table's columns:
//
//   - reading builds the JSON object from the columns (recordDataExpr);
//   - writing spreads the JSON payload over the columns with jsonb_to_record, typed by each
//     field's kind, so Postgres does the casting and a value that does not fit its column is an
//     error, not silently stored.
//
// Every statement aliases the table as t so the record's own columns never clash with the
// jsonb_to_record alias (r) that carries the same field names.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metap-org/metap/pkg/metadata"
)

// frameworkFieldColumn: the two field names that live in framework columns rather than a column
// of their own. CrudService mirrors a code-holding or workflow-state field's value into them.
func frameworkFieldColumn(name string) bool { return name == "code" || name == "status" }

// nativeFields are the fields that own a column: every field except code/status.
func nativeFields(entity metadata.EntityDefinition) []metadata.EntityField {
	out := make([]metadata.EntityField, 0, len(entity.Fields))
	for _, f := range entity.Fields {
		if !frameworkFieldColumn(f.Name) {
			out = append(out, f)
		}
	}
	return out
}

// writableFields are the native fields a normal write (update, transition) sets. System fields
// keep whatever is stored *now*: a concurrent SetSystemFields does not bump version, so the version
// check alone would not stop this write from putting a stale system value back.
func writableFields(entity metadata.EntityDefinition) []metadata.EntityField {
	system := map[string]bool{}
	for _, n := range metadata.SystemFieldNames(entity) {
		system[n] = true
	}
	out := make([]metadata.EntityField, 0, len(entity.Fields))
	for _, f := range nativeFields(entity) {
		if !system[f.Name] {
			out = append(out, f)
		}
	}
	return out
}

func quoteCol(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// recordDataExpr builds the record's JSON object from its columns. A NULL column is left out (the
// same as a field the payload never carried); a JSON-kind column holding a JSON null stays a
// value. jsonb_strip_nulls is deliberately not used: it is recursive and would rewrite the
// contents of a JSON field.
func recordDataExpr(entity metadata.EntityDefinition) string {
	if len(entity.Fields) == 0 {
		return "'{}'::jsonb"
	}
	pairs := make([]string, 0, len(entity.Fields))
	for _, f := range entity.Fields {
		col := "t." + quoteCol(f.Name)
		if frameworkFieldColumn(f.Name) {
			col = "t." + f.Name
		}
		pairs = append(pairs, fmt.Sprintf("(%s, to_jsonb(%s))", quoteLit(f.Name), col))
	}
	return "(SELECT COALESCE(jsonb_object_agg(x.k, x.v), '{}'::jsonb) FROM (VALUES " +
		strings.Join(pairs, ", ") + ") AS x(k, v) WHERE x.v IS NOT NULL)"
}

func quoteLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// recordSelectList is the column list every record read returns, in the order scanRecordRow
// expects. Callers alias the table as t.
func recordSelectList(entity metadata.EntityDefinition) string {
	return "t.id, t.code, t.status, " + recordDataExpr(entity) + " AS data, t.version, t.created_at, t.updated_at"
}

// recordTypeList is the column definition list for jsonb_to_record: `"f" type, ...`.
func recordTypeList(fields []metadata.EntityField) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = quoteCol(f.Name) + " " + metadata.FieldKindSQLType(f.Kind)
	}
	return strings.Join(parts, ", ")
}

// plainStatusValue: an entity with no workflow may still declare an ordinary field named
// "status"; its value lives in the status column. Workflow entities own that column through the
// state machine, so this returns nil for them.
func plainStatusValue(entity metadata.EntityDefinition, data JsonObject) *string {
	if entity.Workflow != nil {
		return nil
	}
	for _, f := range entity.Fields {
		if f.Name != "status" {
			continue
		}
		if raw, ok := data["status"]; ok {
			var s string
			if jsonUnmarshalString(raw, &s) {
				return &s
			}
		}
	}
	return nil
}

// buildInsertSQL: parameters are $1 tenant_id, $2 code, $3 status, $4 created_by, $5 updated_by,
// $6 the payload as JSON.
func buildInsertSQL(entity metadata.EntityDefinition) string {
	fields := nativeFields(entity)
	if len(fields) == 0 {
		return fmt.Sprintf(
			"INSERT INTO %s AS t (tenant_id, code, status, created_by, updated_by) SELECT $1::uuid, $2::varchar, $3::varchar, $4::uuid, $5::uuid WHERE $6::jsonb IS NOT NULL RETURNING %s",
			entity.TableName, recordSelectList(entity))
	}
	cols := make([]string, len(fields))
	vals := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = quoteCol(f.Name)
		vals[i] = "r." + quoteCol(f.Name)
	}
	return fmt.Sprintf(
		"INSERT INTO %s AS t (tenant_id, code, status, created_by, updated_by, %s) "+
			"SELECT $1::uuid, $2::varchar, $3::varchar, $4::uuid, $5::uuid, %s FROM jsonb_to_record($6::jsonb) AS r(%s) RETURNING %s",
		entity.TableName, strings.Join(cols, ", "), strings.Join(vals, ", "), recordTypeList(fields), recordSelectList(entity))
}

// buildUpdateSQL covers update and transition: both rewrite the record's writable fields from the
// full merged payload and bump the version. extraSet is one more assignment (code = $6 for an
// update, status = $6 for a transition). Parameters: $1 payload JSON, $2 updated_by, $3 id,
// $4 tenant_id, $5 expected version, $6 whatever extraSet binds.
func buildUpdateSQL(entity metadata.EntityDefinition, extraSet string) string {
	fields := writableFields(entity)
	sets := make([]string, 0, len(fields)+5)
	for _, f := range fields {
		sets = append(sets, quoteCol(f.Name)+" = r."+quoteCol(f.Name))
	}
	sets = append(sets, extraSet, "version = t.version + 1", "updated_at = now()", "updated_by = $2::uuid")
	from, guard := "", ""
	if len(fields) > 0 {
		from = fmt.Sprintf(" FROM jsonb_to_record($1::jsonb) AS r(%s)", recordTypeList(fields))
	} else {
		// $1 must still be referenced or Postgres cannot type the parameter.
		guard = " AND $1::jsonb IS NOT NULL"
	}
	return fmt.Sprintf(
		"UPDATE %s AS t SET %s%s WHERE t.id = $3 AND t.tenant_id = $4 AND t.version = $5 AND t.deleted = false%s RETURNING %s",
		entity.TableName, strings.Join(sets, ", "), from, guard, recordSelectList(entity))
}

func jsonUnmarshalString(raw json.RawMessage, out *string) bool {
	return json.Unmarshal(raw, out) == nil
}
