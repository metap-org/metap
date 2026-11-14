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
	// OpDropSyncTrigger removes a pre-Phase-106 trigger (and its function) that copied
	// data ->> field into a real column. Such a trigger would overwrite a natively written
	// column with NULL, so it must go before native writes start. Idempotent.
	OpDropSyncTrigger
	// OpBackfillColumn is a checkpointed, resumable copy of data ->> field into a native column
	// on a table that still has the legacy data column. OpID keys
	// metadata.reconciler_backfill_progress.
	OpBackfillColumn
	OpCreateIndexConcurrently
	OpDropIndexConcurrently
	OpAddForeignKeyNotValid
	OpValidateForeignKey
	OpDropForeignKey
	OpAddUnique
	OpDropUnique
)

type DdlOp struct {
	Kind DdlOpKind

	// AddColumn
	ColumnName string
	ColumnSpec ColumnSpec

	// RenameColumn
	RenameFrom string
	RenameTo   string

	// DropSyncTrigger
	SyncField string

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

	// Step 2: columns — add missing, never drop. While the table still has the legacy data
	// column (HasLegacyData), a native column is also filled from data, resumably; the old sync
	// triggers are dropped last so a writer still on the old code keeps both copies in step
	// until the migration finishes.
	for _, name := range sortedKeys(desired.Columns) {
		spec := desired.Columns[name]
		actualSpec, exists := actualCopy.Columns[name]
		if !exists {
			ops = append(ops, DdlOp{Kind: OpAddColumn, ColumnName: name, ColumnSpec: spec})
		}
		if spec.Origin.Kind != OriginNative || !actualCopy.HasLegacyData {
			continue
		}
		if !exists || !actualSpec.Origin.Backfilled {
			ops = append(ops, DdlOp{
				Kind: OpBackfillColumn, BackfillOpID: BackfillOpID(desired.Table, name),
				BackfillColumn: name, BackfillSourceField: name, BackfillSQLType: spec.SQLType,
			})
		}
	}
	for _, field := range actualCopy.LegacySyncTriggers {
		ops = append(ops, DdlOp{Kind: OpDropSyncTrigger, SyncField: field})
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

// Every drop in the FK/unique/index family ranks strictly before every create/add in that family —
// not paired same-rank with its own create/add — because a same-*name* transition from a UNIQUE
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
		case OpBackfillColumn:
			return 3
		case OpDropSyncTrigger:
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
	out.HasLegacyData = s.HasLegacyData
	out.LegacySyncTriggers = append([]string(nil), s.LegacySyncTriggers...)
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

// TypeMismatch is a native column whose existing SQL type differs from what the EntityDefinition now
// asks for (a field's kind was changed). Diff never alters a column's type on its own: that rewrites
// the table and can fail on existing values, so it is the explicit widen_type migration's job.
type TypeMismatch struct {
	Column, Have, Want string
}

// ColumnTypeMismatches lists every native column that exists with a different type than desired.
func ColumnTypeMismatches(desired PhysicalSchema, actual *PhysicalSchema) []TypeMismatch {
	if actual == nil {
		return nil
	}
	var out []TypeMismatch
	for _, name := range sortedKeys(desired.Columns) {
		want := desired.Columns[name]
		have, ok := actual.Columns[name]
		if !ok || want.Origin.Kind != OriginNative {
			continue
		}
		if have.SQLType != want.SQLType {
			out = append(out, TypeMismatch{Column: name, Have: have.SQLType, Want: want.SQLType})
		}
	}
	return out
}

// IsDestructive: the op removes a guarantee or an object rather than adding one (an FK, a unique
// constraint or a sync trigger goes away). Dropping an index only costs speed and is not counted.
// Nothing Diff emits ever drops a column or data.
func (op DdlOp) IsDestructive() bool {
	switch op.Kind {
	case OpDropForeignKey, OpDropUnique, OpDropSyncTrigger:
		return true
	default:
		return false
	}
}
