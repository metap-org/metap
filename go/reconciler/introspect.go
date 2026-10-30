// Introspect(actual) -> (*PhysicalSchema, bool) — the actual-state half of the reconcile
// pipeline, read straight from pg_catalog. (nil, false) means the table doesn't exist yet
// (Diff's step 0: emit a CreateTable op).
package reconciler

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Introspect(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, table string) (*PhysicalSchema, error) {
	// table is schema-qualified (compile.QualifiedTableNameFor, e.g. "entities.jira_issues") —
	// split once for the pg_catalog/information_schema queries below, which key on bare table
	// name + schema name separately, never a dotted string.
	schema, bareTable, ok := strings.Cut(table, ".")
	if !ok {
		schema, bareTable = "public", table
	}

	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	frameworkNames := map[string]bool{}
	for _, name := range FrameworkColumnNames() {
		frameworkNames[name] = true
	}

	result := EmptySchema(table)

	columnRows, err := pool.Query(ctx,
		`SELECT column_name, data_type, is_nullable, character_maximum_length, numeric_precision, numeric_scale
		 FROM information_schema.columns WHERE table_schema = $2 AND table_name = $1`,
		bareTable, schema)
	if err != nil {
		return nil, err
	}
	type colRow struct {
		name, dataType, isNullable      string
		charLen, numPrecision, numScale *int32
	}
	var colRows []colRow
	for columnRows.Next() {
		var r colRow
		if err := columnRows.Scan(&r.name, &r.dataType, &r.isNullable, &r.charLen, &r.numPrecision, &r.numScale); err != nil {
			columnRows.Close()
			return nil, err
		}
		colRows = append(colRows, r)
	}
	columnRows.Close()
	if err := columnRows.Err(); err != nil {
		return nil, err
	}
	for _, r := range colRows {
		sqlType := pgTypeToShort(r.dataType, r.charLen, r.numPrecision, r.numScale)
		var origin ColumnOrigin
		if frameworkNames[r.name] {
			origin = ColumnOrigin{Kind: OriginFramework}
		} else {
			// reconciler_backfill_progress.completed alone isn't enough — a Generated column
			// only reads back as truly backfilled when the ledger says so *and* its sync
			// trigger is actually present (see this function's own doc comment history for the
			// real incident this closes). Either half being false means Diff's existing
			// push_sync_and_backfill re-assertion path fires again on the very next reconcile.
			completed, err := isBackfillCompleted(ctx, pool, tenantID, entityName, BackfillOpID(table, r.name))
			if err != nil {
				return nil, err
			}
			triggerExists := false
			if completed {
				triggerExists, err = syncTriggerExists(ctx, pool, schema, bareTable, r.name)
				if err != nil {
					return nil, err
				}
			}
			origin = ColumnOrigin{Kind: OriginGenerated, SourceField: r.name, Backfilled: completed && triggerExists}
		}
		result.Columns[r.name] = ColumnSpec{SQLType: sqlType, Nullable: r.isNullable == "YES", Origin: origin}
	}

	indexRows, err := pool.Query(ctx,
		`SELECT ix.relname AS index_name, pg_get_indexdef(ix.oid) AS indexdef, i.indisvalid, i.indisunique, am.amname
		 FROM pg_index i
		 JOIN pg_class ix ON ix.oid = i.indexrelid
		 JOIN pg_class tbl ON tbl.oid = i.indrelid
		 JOIN pg_am am ON am.oid = ix.relam
		 JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		 WHERE tbl.relname = $1 AND ns.nspname = $2 AND NOT i.indisprimary`,
		bareTable, schema)
	if err != nil {
		return nil, err
	}
	for indexRows.Next() {
		var indexName, indexdef, amname string
		var valid, unique bool
		if err := indexRows.Scan(&indexName, &indexdef, &valid, &unique, &amname); err != nil {
			indexRows.Close()
			return nil, err
		}
		expression, whereClause := extractIndexExpressionAndWhere(indexdef)
		using := ""
		if amname != "btree" {
			using = amname
		}
		result.Indexes[indexName] = IndexSpec{Expression: expression, Unique: unique, Using: using, Valid: valid, WhereClause: whereClause}
	}
	indexRows.Close()
	if err := indexRows.Err(); err != nil {
		return nil, err
	}

	constraintRows, err := pool.Query(ctx,
		`SELECT c.conname, c.contype::text AS contype, c.convalidated,
		        array(SELECT attname FROM pg_attribute WHERE attrelid = c.conrelid AND attnum = ANY(c.conkey)) AS cols,
		        ref.relname AS ref_table,
		        refns.nspname AS ref_schema,
		        array(SELECT attname FROM pg_attribute WHERE attrelid = c.confrelid AND attnum = ANY(c.confkey)) AS ref_cols,
		        c.confdeltype::text AS confdeltype
		 FROM pg_constraint c
		 JOIN pg_class tbl ON tbl.oid = c.conrelid
		 JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		 LEFT JOIN pg_class ref ON ref.oid = c.confrelid
		 LEFT JOIN pg_namespace refns ON refns.oid = ref.relnamespace
		 WHERE tbl.relname = $1 AND ns.nspname = $2 AND c.contype IN ('f', 'u')`,
		bareTable, schema)
	if err != nil {
		return nil, err
	}
	for constraintRows.Next() {
		var conname, contype, refTable, confdeltype string
		var convalidated bool
		var cols, refCols []string
		var refSchema *string
		if err := constraintRows.Scan(&conname, &contype, &convalidated, &cols, &refTable, &refSchema, &refCols, &confdeltype); err != nil {
			constraintRows.Close()
			return nil, err
		}
		switch contype {
		case "f":
			// compile()'s desired FkSpec.RefTable is always schema-qualified, or Diff would see
			// every FK's RefTable as "changed" on every pass and never converge.
			qualifiedRefTable := refTable
			if refSchema != nil {
				qualifiedRefTable = *refSchema + "." + refTable
			}
			column, refColumn := "", ""
			if len(cols) > 0 {
				column = cols[0]
			}
			if len(refCols) > 0 {
				refColumn = refCols[0]
			}
			result.ForeignKeys[conname] = FkSpec{
				Column: column, RefTable: qualifiedRefTable, RefColumn: refColumn,
				OnDelete: onDeleteFromChar(confdeltype), Validated: convalidated,
			}
		case "u":
			result.Uniques[conname] = UniqueSpec{Columns: cols}
		}
	}
	constraintRows.Close()
	if err := constraintRows.Err(); err != nil {
		return nil, err
	}

	return &result, nil
}

func BackfillOpID(table, column string) string {
	return fmt.Sprintf("backfill:%s:%s", table, column)
}

// syncTriggerExists: whether the BEFORE INSERT OR UPDATE sync trigger executor.go's
// buildSyncTriggerSQL creates for a Generated column actually exists in pg_catalog right now.
// Trigger name (trg_sync_{bareTable}_{field}, unquoted) must match buildSyncTriggerSQL's naming
// exactly.
func syncTriggerExists(ctx context.Context, pool *pgxpool.Pool, schema, bareTable, field string) (bool, error) {
	triggerName := fmt.Sprintf("trg_sync_%s_%s", bareTable, field)
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND t.tgname = $3 AND NOT t.tgisinternal
		)`, schema, bareTable, triggerName).Scan(&exists)
	return exists, err
}

func isBackfillCompleted(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, opID string) (bool, error) {
	var completed *bool
	err := pool.QueryRow(ctx,
		`SELECT completed FROM metadata.reconciler_backfill_progress
		 WHERE tenant_id = $1 AND entity_name = $2 AND op_id = $3`,
		tenantID, entityName, opID).Scan(&completed)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return false, nil
		}
		return false, err
	}
	return completed != nil && *completed, nil
}

func onDeleteFromChar(confdeltype string) OnDelete {
	switch confdeltype {
	case "c":
		return OnDeleteCascade
	case "n":
		return OnDeleteSetNull
	default:
		return OnDeleteRestrict
	}
}

// pgTypeToShort reconstructs the short type string Compile uses ("varchar(120)", "timestamptz",
// "numeric(18,4)", ...) from information_schema.columns' verbose SQL-standard names, so both
// sides of Diff's column comparison speak the same vocabulary.
func pgTypeToShort(dataType string, charLen, numericPrecision, numericScale *int32) string {
	switch dataType {
	case "character varying":
		if charLen != nil {
			return fmt.Sprintf("varchar(%d)", *charLen)
		}
		return "varchar"
	case "timestamp with time zone":
		return "timestamptz"
	case "numeric":
		if numericPrecision != nil && numericScale != nil {
			return fmt.Sprintf("numeric(%d,%d)", *numericPrecision, *numericScale)
		}
		return "numeric"
	default:
		return dataType
	}
}

// extractIndexExpressionAndWhere: pg_get_indexdef returns the *whole* CREATE INDEX ... statement
// — this only wants the column/expression list (plus any trailing opclass, which sits *outside*
// the expression's own parens) and, separately, a partial index's WHERE predicate. Format is
// always "... USING <method> (<expr>)[ <opclass>][ WHERE (<predicate>)]".
func extractIndexExpressionAndWhere(indexdef string) (string, string) {
	const usingMarker = " USING "
	usingPos := strings.Index(indexdef, usingMarker)
	if usingPos < 0 {
		return indexdef, ""
	}
	afterUsing := indexdef[usingPos+len(usingMarker):]
	parenStart := strings.IndexByte(afterUsing, '(')
	if parenStart < 0 {
		return strings.TrimSpace(afterUsing), ""
	}
	exprRegion := afterUsing[parenStart:]
	parenEnd := matchingCloseParen(exprRegion)
	if parenEnd < 0 {
		return strings.TrimSpace(afterUsing), ""
	}
	parenExpr := exprRegion[:parenEnd+1]
	rest := strings.TrimSpace(exprRegion[parenEnd+1:])
	if predicate, ok := strings.CutPrefix(rest, "WHERE "); ok {
		return parenExpr, strings.TrimSpace(predicate)
	}
	if rest == "" {
		return parenExpr, ""
	}
	// No WHERE — rest is a trailing opclass (gin_trgm_ops); belongs on the expression side.
	return parenExpr + " " + rest, ""
}

// matchingCloseParen finds the index (into s) of the ')' that closes the '(' at s's start,
// accounting for nesting — s must start with '('. Returns -1 if not found.
func matchingCloseParen(s string) int {
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
