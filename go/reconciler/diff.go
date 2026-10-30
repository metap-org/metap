// Diff(desired, actual, renames) -> []DdlOp — compares two PhysicalSchema values (never an
// EntityDefinition against raw pg_catalog), normalizing expressions at comparison time so
// Postgres's own canonicalization of what was typed never false-positives a rebuild.
package reconciler

import "sort"

// DdlOpKind discriminates DdlOp's variants — Go has no sum type, so DdlOp is a flat struct with
// a Kind tag and only the fields that variant actually uses populated (mirroring the pattern
// this port already uses for permission.PolicyCondition / crud.ServiceResult).
type DdlOpKind int

const (
	OpCreateTable DdlOpKind = iota
	OpAddColumn
	OpRenameColumn
	// OpAddSyncTrigger (re)creates the BEFORE INSERT OR UPDATE trigger that keeps a
	// storage:column field's real column in sync with data ->> field — the permanent stand-in
	// for Postgres's GENERATED ... STORED, which this crate never uses. Idempotent (CREATE OR
	// REPLACE TRIGGER) — safe to re-run on resume.
	OpAddSyncTrigger
	// OpBackfillColumn is a checkpointed, resumable backfill of a newly (or not-yet-fully)
	// populated real column. OpID keys metadata.reconciler_backfill_progress.
	OpBackfillColumn
	OpCreateIndexConcurrently
	OpDropIndexConcurrently
	OpAddForeignKeyNotValid
	OpValidateForeignKey
	OpDropForeignKey
	OpAddUnique
	OpDropUnique
)

// DdlOp mirrors the Rust DdlOp enum.
type DdlOp struct {
	Kind DdlOpKind

	// AddColumn
	ColumnName string
	ColumnSpec ColumnSpec

	// RenameColumn
	RenameFrom string
	RenameTo   string

	// AddSyncTrigger
	SyncField   string
	SyncSQLType string

	// BackfillColumn
	BackfillOpID        string
	BackfillColumn      string
	BackfillSourceField string
	BackfillSQLType     string

	// CreateIndexConcurrently / DropIndexConcurrently
	IndexName string
	IndexSpec IndexSpec

	// AddForeignKeyNotValid / ValidateForeignKey / DropForeignKey
	FkName string
	FkSpec FkSpec

	// AddUnique / DropUnique
	UniqueName string
	UniqueSpec UniqueSpec
}

// Cost governs whether the executor must set Migrating before running this op.
func (op DdlOp) Cost() Cost {
	switch op.Kind {
	case OpBackfillColumn:
		return CostHeavy
	case OpCreateIndexConcurrently, OpDropIndexConcurrently, OpValidateForeignKey:
		return CostBackgroundFast
	default:
		return CostInstant
	}
}

// ExecutionMode governs how the executor runs this op.
func (op DdlOp) ExecutionMode() ExecutionMode {
	switch op.Kind {
	case OpCreateIndexConcurrently, OpDropIndexConcurrently:
		return ModeNonTransactional
	case OpBackfillColumn:
		return ModeBatched
	default:
		return ModeTransactional
	}
}

// Diff mirrors the Rust diff(). actual == nil means the table doesn't exist yet — step 0: emit
// CreateTable, then diff the rest against an empty schema. renames is caller-supplied (there is
// no way to *infer* a rename from two column sets alone) and is applied first so every step
// after it never sees the stale name.
func Diff(desired PhysicalSchema, actual *PhysicalSchema, renames [][2]string) []DdlOp {
	var ops []DdlOp
	var actualCopy PhysicalSchema
	if actual == nil {
		ops = append(ops, DdlOp{Kind: OpCreateTable})
		actualCopy = EmptySchema(desired.Table)
	} else {
		actualCopy = clonePhysicalSchema(*actual)
	}

	// Step 1: renames, applied to a mutable copy of actual so every later step already sees the
	// new name.
	for _, rn := range renames {
		from, to := rn[0], rn[1]
		if _, ok := actualCopy.Columns[to]; ok {
			continue
		}
		spec, ok := actualCopy.Columns[from]
		if !ok {
			continue
		}
		if _, ok := desired.Columns[to]; !ok {
			continue
		}
		delete(actualCopy.Columns, from)
		actualCopy.Columns[to] = spec
		ops = append(ops, DdlOp{Kind: OpRenameColumn, RenameFrom: from, RenameTo: to})
	}

	// Step 2: columns — add missing, never drop. A Generated column that exists but isn't fully
	// backfilled yet (crash-resume) gets its trigger re-asserted and its backfill re-queued.
	for _, name := range sortedKeys(desired.Columns) {
		spec := desired.Columns[name]
		actualSpec, exists := actualCopy.Columns[name]
		if !exists {
			ops = append(ops, DdlOp{Kind: OpAddColumn, ColumnName: name, ColumnSpec: spec})
			if spec.Origin.Kind == OriginGenerated {
				ops = pushSyncAndBackfill(ops, desired.Table, name, spec.Origin.SourceField, spec.SQLType)
			}
		} else if actualSpec.Origin.Kind == OriginGenerated && !actualSpec.Origin.Backfilled {
			ops = pushSyncAndBackfill(ops, desired.Table, name, actualSpec.Origin.SourceField, spec.SQLType)
		}
	}

	// Step 3: indexes.
	for _, name := range sortedKeys(desired.Indexes) {
		spec := desired.Indexes[name]
		actualIdx, exists := actualCopy.Indexes[name]
		if !exists {
			ops = append(ops, DdlOp{Kind: OpCreateIndexConcurrently, IndexName: name, IndexSpec: spec})
		} else if !actualIdx.Valid || !indexMatches(actualIdx, spec) {
			ops = append(ops, DdlOp{Kind: OpDropIndexConcurrently, IndexName: name})
			ops = append(ops, DdlOp{Kind: OpCreateIndexConcurrently, IndexName: name, IndexSpec: spec})
		}
	}
	for _, name := range sortedKeys(actualCopy.Indexes) {
		// A UNIQUE/PRIMARY KEY constraint automatically owns a backing index under its own name
		// — pg_indexes sees it like any other index, but it isn't a separate object this diff
		// manages: Postgres won't let a plain DROP INDEX remove it while the constraint exists
		// (dropping the constraint via DropUnique already takes the index with it).
		if _, wanted := desired.Indexes[name]; !wanted {
			if _, backedByUnique := actualCopy.Uniques[name]; !backedByUnique {
				ops = append(ops, DdlOp{Kind: OpDropIndexConcurrently, IndexName: name})
			}
		}
	}

	// Step 4: foreign keys. A brand-new FK gets *both* AddForeignKeyNotValid and
	// ValidateForeignKey in this same pass — the "resume, only VALIDATE" rule is specifically
	// for the crash-recovery case where a *previous* pass's AddForeignKeyNotValid already
	// committed but its ValidateForeignKey never ran.
	for _, name := range sortedKeys(desired.ForeignKeys) {
		spec := desired.ForeignKeys[name]
		actualFk, exists := actualCopy.ForeignKeys[name]
		if !exists {
			ops = append(ops, DdlOp{Kind: OpAddForeignKeyNotValid, FkName: name, FkSpec: spec})
			ops = append(ops, DdlOp{Kind: OpValidateForeignKey, FkName: name})
		} else if !fkMatches(actualFk, spec) {
			ops = append(ops, DdlOp{Kind: OpDropForeignKey, FkName: name})
			ops = append(ops, DdlOp{Kind: OpAddForeignKeyNotValid, FkName: name, FkSpec: spec})
			ops = append(ops, DdlOp{Kind: OpValidateForeignKey, FkName: name})
		} else if !actualFk.Validated {
			// Crash between NOT VALID and VALIDATE -> only VALIDATE, no duplicate add.
			ops = append(ops, DdlOp{Kind: OpValidateForeignKey, FkName: name})
		}
	}
	for _, name := range sortedKeys(actualCopy.ForeignKeys) {
		if _, wanted := desired.ForeignKeys[name]; !wanted {
			ops = append(ops, DdlOp{Kind: OpDropForeignKey, FkName: name})
		}
	}

	// Step 5: unique constraints (not the unique *indexes* created above for a storage:column
	// field — those already enforce uniqueness on their own; this is the separately-named
	// constraint Compile also emits for that case).
	for _, name := range sortedKeys(desired.Uniques) {
		spec := desired.Uniques[name]
		actualUq, exists := actualCopy.Uniques[name]
		if !exists {
			ops = append(ops, DdlOp{Kind: OpAddUnique, UniqueName: name, UniqueSpec: spec})
		} else if !stringSlicesEqual(actualUq.Columns, spec.Columns) {
			ops = append(ops, DdlOp{Kind: OpDropUnique, UniqueName: name})
			ops = append(ops, DdlOp{Kind: OpAddUnique, UniqueName: name, UniqueSpec: spec})
		}
	}
	for _, name := range sortedKeys(actualCopy.Uniques) {
		if _, wanted := desired.Uniques[name]; !wanted {
			ops = append(ops, DdlOp{Kind: OpDropUnique, UniqueName: name})
		}
	}

	return topoSort(ops)
}

func pushSyncAndBackfill(ops []DdlOp, table, column, sourceField, sqlType string) []DdlOp {
	ops = append(ops, DdlOp{Kind: OpAddSyncTrigger, SyncField: sourceField, SyncSQLType: sqlType})
	ops = append(ops, DdlOp{
		Kind: OpBackfillColumn, BackfillOpID: BackfillOpID(table, column),
		BackfillColumn: column, BackfillSourceField: sourceField, BackfillSQLType: sqlType,
	})
	return ops
}

func indexMatches(actual, desired IndexSpec) bool {
	return NormalizeExpr(actual.Expression) == NormalizeExpr(desired.Expression) &&
		actual.Unique == desired.Unique &&
		actual.Using == desired.Using &&
		whereClauseMatches(actual.WhereClause, desired.WhereClause)
}

func whereClauseMatches(actual, desired string) bool {
	if actual == "" && desired == "" {
		return true
	}
	if actual == "" || desired == "" {
		return false
	}
	return NormalizeExpr(actual) == NormalizeExpr(desired)
}

func fkMatches(actual, desired FkSpec) bool {
	return actual.Column == desired.Column && actual.RefTable == desired.RefTable &&
		actual.RefColumn == desired.RefColumn && actual.OnDelete == desired.OnDelete
}

// topoSort mirrors the Rust CreateTable -> AddColumn -> ... -> ValidateFK ordering. Every drop
// in the FK/unique/index family ranks strictly before every create/add in that family — not
// paired same-rank with its own create/add — because a same-*name* transition from a UNIQUE
// constraint to a plain index needs its DropUnique to run *before* any
// DropIndexConcurrently/CreateIndexConcurrently touching that name.
func topoSort(ops []DdlOp) []DdlOp {
	rank := func(op DdlOp) int {
		switch op.Kind {
		case OpCreateTable:
			return 0
		case OpRenameColumn:
			return 1
		case OpAddColumn:
			return 2
		case OpAddSyncTrigger:
			return 3
		case OpBackfillColumn:
			return 4
		case OpDropForeignKey:
			return 5
		case OpDropUnique:
			return 6
		case OpDropIndexConcurrently:
			return 7
		case OpCreateIndexConcurrently:
			return 8
		case OpAddUnique:
			return 9
		case OpAddForeignKeyNotValid:
			return 10
		case OpValidateForeignKey:
			return 11
		default:
			return 99
		}
	}
	sort.SliceStable(ops, func(i, j int) bool { return rank(ops[i]) < rank(ops[j]) })
	return ops
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func clonePhysicalSchema(s PhysicalSchema) PhysicalSchema {
	out := EmptySchema(s.Table)
	for k, v := range s.Columns {
		out.Columns[k] = v
	}
	for k, v := range s.Indexes {
		out.Indexes[k] = v
	}
	for k, v := range s.ForeignKeys {
		out.ForeignKeys[k] = v
	}
	for k, v := range s.Uniques {
		cols := make([]string, len(v.Columns))
		copy(cols, v.Columns)
		out.Uniques[k] = UniqueSpec{Columns: cols}
	}
	return out
}
