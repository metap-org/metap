package reconciler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func diffCol(sqlType string) ColumnSpec {
	return ColumnSpec{SQLType: sqlType, Nullable: true, Origin: ColumnOrigin{Kind: OriginFramework}}
}

func diffIdx(expression string) IndexSpec {
	return IndexSpec{Expression: expression, Valid: true}
}

func findOp(ops []DdlOp, pred func(DdlOp) bool) (int, bool) {
	for i, op := range ops {
		if pred(op) {
			return i, true
		}
	}
	return -1, false
}

func TestNoActualTableCreatesTableAndEveryDesiredPiece(t *testing.T) {
	desired := EmptySchema("hr_employees")
	desired.Columns["id"] = diffCol("uuid")
	desired.Indexes["idx_hr_employees_dept"] = diffIdx("(data ->> 'dept')::text")

	ops := Diff(desired, nil, nil)
	require.Equal(t, OpCreateTable, ops[0].Kind, "expected first op CreateTable, got %+v", ops[0])
	_, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddColumn && o.ColumnName == "id" })
	require.True(t, ok, "expected AddColumn id")
	_, ok = findOp(ops, func(o DdlOp) bool {
		return o.Kind == OpCreateIndexConcurrently && o.IndexName == "idx_hr_employees_dept"
	})
	require.True(t, ok, "expected CreateIndexConcurrently idx_hr_employees_dept")
}

func TestMatchingActualProducesNoOps(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["id"] = diffCol("uuid")
	desired.Indexes["idx_t_f"] = diffIdx("(data ->> 'f')::text")
	actual := clonePhysicalSchema(desired)

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 0, "expected no ops, got %+v", ops)
}

func TestMatchingAfterNormalizationProducesNoOpsEvenWhenRawStringsDiffer(t *testing.T) {
	desired := EmptySchema("t")
	desired.Indexes["idx_t_f"] = diffIdx("(data ->> 'f')::text")
	actual := EmptySchema("t")
	actual.Indexes["idx_t_f"] = diffIdx("((data ->> 'f'::text))")

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 0, "expected no ops, got %+v", ops)
}

func TestInvalidIndexIsDroppedThenRecreatedDropStrictlyBeforeCreate(t *testing.T) {
	desired := EmptySchema("t")
	desired.Indexes["idx_t_f"] = diffIdx("(data ->> 'f')::text")
	actual := clonePhysicalSchema(desired)
	idx := actual.Indexes["idx_t_f"]
	idx.Valid = false
	actual.Indexes["idx_t_f"] = idx

	ops := Diff(desired, &actual, nil)
	dropPos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropIndexConcurrently })
	createPos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpCreateIndexConcurrently })
	require.False(t, dropPos >= createPos, "expected drop before create, got drop=%d create=%d", dropPos, createPos)
}

func TestUniqueConstraintToPartialIndexSameNameDropsConstraintBeforeRebuildingIndex(t *testing.T) {
	name := "uniq_t_field"
	desired := EmptySchema("t")
	desired.Indexes[name] = IndexSpec{Expression: "field", Unique: true, Valid: true, WhereClause: "deleted = false"}

	actual := EmptySchema("t")
	actual.Indexes[name] = IndexSpec{Expression: "field", Unique: true, Valid: true}
	actual.Uniques[name] = UniqueSpec{Columns: []string{"field"}}

	ops := Diff(desired, &actual, nil)
	pDropUnique, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropUnique && o.UniqueName == name })
	pDropIndex, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropIndexConcurrently && o.IndexName == name })
	pCreateIndex, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpCreateIndexConcurrently && o.IndexName == name })

	require.False(t, pDropUnique >= pDropIndex, "DropUnique must run before DropIndexConcurrently for the same name")
	require.False(t, pDropUnique >= pCreateIndex, "DropUnique must run before the new index is created under the same name")
	require.False(t, pDropIndex >= pCreateIndex, "drop strictly before recreate")
}

func TestMissingColumnNotInDesiredIsNeverDropped(t *testing.T) {
	desired := EmptySchema("t")
	actual := EmptySchema("t")
	actual.Columns["stale"] = diffCol("text")

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 0, "expected no ops, got %+v", ops)
}

func TestRenameAppliesBeforeColumnDiffSoNoDropAddPairAppears(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["new_name"] = diffCol("text")
	actual := EmptySchema("t")
	actual.Columns["old_name"] = diffCol("text")

	ops := Diff(desired, &actual, [][2]string{{"old_name", "new_name"}})
	require.Len(t, ops, 1, "got %+v", ops)
	require.Equal(t, OpRenameColumn, ops[0].Kind, "got %+v", ops)
	require.Equal(t, "old_name", ops[0].RenameFrom, "got %+v", ops)
	require.Equal(t, "new_name", ops[0].RenameTo, "got %+v", ops)
}

func TestUnbackfilledColumnOnALegacyTableResumesWithoutReadd(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}
	actual := EmptySchema("t")
	actual.HasLegacyData = true
	actual.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginNative, Backfilled: false}}

	ops := Diff(desired, &actual, nil)
	_, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddColumn })
	require.False(t, ok, "must not re-add an existing column")
	_, ok = findOp(ops, func(o DdlOp) bool { return o.Kind == OpBackfillColumn })
	require.True(t, ok, "must resume the backfill")
}

func TestBackfilledNativeColumnProducesNoOps(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}
	actual := clonePhysicalSchema(desired)

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 0, "expected no ops, got %+v", ops)
}

func TestNewFieldOnANativeTableIsJustAnAddColumnNoBackfill(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true, Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}
	actual := EmptySchema("t")

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 1, "got %+v", ops)
	require.Equal(t, OpAddColumn, ops[0].Kind)
}

func TestLegacyTableGetsColumnBackfillThenSyncTriggersDropped(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true, Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}
	desired.Columns["name"] = ColumnSpec{SQLType: "text", Nullable: true, Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}
	actual := EmptySchema("t")
	actual.HasLegacyData = true
	actual.LegacySyncTriggers = []string{"amount"}
	actual.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true, Origin: ColumnOrigin{Kind: OriginNative, Backfilled: true}}

	ops := Diff(desired, &actual, nil)
	kinds := make([]DdlOpKind, len(ops))
	for i, o := range ops {
		kinds[i] = o.Kind
	}
	require.Equal(t, []DdlOpKind{OpAddColumn, OpBackfillColumn, OpDropSyncTrigger}, kinds, "add and fill the missing column, drop the old trigger last: %+v", ops)
	require.Equal(t, "name", ops[1].BackfillColumn)
	require.Equal(t, "amount", ops[2].SyncField)
}

func TestFkAddedNotValidThenValidatedWhenMissing(t *testing.T) {
	desired := EmptySchema("hr_employees")
	desired.ForeignKeys["fk_hr_employees_departmentid"] = FkSpec{
		Column: "departmentId", RefTable: "hr_departments", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true,
	}
	ops := Diff(desired, nil, nil)
	addPos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddForeignKeyNotValid })
	validatePos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpValidateForeignKey })
	require.False(t, addPos >= validatePos, "a brand-new FK must be added then validated in the same pass")
}

func TestFkAddedButNotValidatedOnlyValidatesNoDuplicateAdd(t *testing.T) {
	desired := EmptySchema("t")
	desired.ForeignKeys["fk_t_x"] = FkSpec{Column: "x", RefTable: "other", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true}
	actual := clonePhysicalSchema(desired)
	fk := actual.ForeignKeys["fk_t_x"]
	fk.Validated = false
	actual.ForeignKeys["fk_t_x"] = fk

	ops := Diff(desired, &actual, nil)
	require.Len(t, ops, 1, "got %+v", ops)
	require.Equal(t, OpValidateForeignKey, ops[0].Kind, "got %+v", ops)
	require.Equal(t, "fk_t_x", ops[0].FkName, "got %+v", ops)
}

func TestOrphanIndexAndFkAndUniqueAreDropped(t *testing.T) {
	desired := EmptySchema("t")
	actual := EmptySchema("t")
	actual.Indexes["idx_gone"] = diffIdx("x")
	actual.ForeignKeys["fk_gone"] = FkSpec{Column: "x", RefTable: "other", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true}
	actual.Uniques["uq_gone"] = UniqueSpec{Columns: []string{"x"}}

	ops := Diff(desired, &actual, nil)
	_, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropIndexConcurrently && o.IndexName == "idx_gone" })
	require.True(t, ok, "expected idx_gone dropped")
	_, ok = findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropForeignKey && o.FkName == "fk_gone" })
	require.True(t, ok, "expected fk_gone dropped")
	_, ok = findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropUnique && o.UniqueName == "uq_gone" })
	require.True(t, ok, "expected uq_gone dropped")
}

func TestTopoOrderCreateTableBeforeColumnsBeforeIndexesBeforeFks(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["id"] = diffCol("uuid")
	desired.Indexes["idx_t_f"] = diffIdx("f")
	desired.ForeignKeys["fk_t_f"] = FkSpec{Column: "f", RefTable: "other", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true}

	ops := Diff(desired, nil, nil)
	pTable, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpCreateTable })
	pCol, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddColumn })
	pIdx, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpCreateIndexConcurrently })
	pFk, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddForeignKeyNotValid })
	require.True(t, (pTable < pCol && pCol < pIdx && pIdx < pFk), "got table=%d col=%d idx=%d fk=%d", pTable, pCol, pIdx, pFk)
}

func TestColumnTypeMismatchesReportsOnlyNativeColumnsWhoseTypeDiffers(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["qty"] = ColumnSpec{SQLType: "double precision", Origin: ColumnOrigin{Kind: OriginNative}}
	desired.Columns["same"] = ColumnSpec{SQLType: "text", Origin: ColumnOrigin{Kind: OriginNative}}
	desired.Columns["status"] = ColumnSpec{SQLType: "varchar(80)", Origin: ColumnOrigin{Kind: OriginFramework}}
	actual := EmptySchema("t")
	actual.Columns["qty"] = ColumnSpec{SQLType: "text"}
	actual.Columns["same"] = ColumnSpec{SQLType: "text"}
	actual.Columns["status"] = ColumnSpec{SQLType: "text"}

	require.Equal(t, []TypeMismatch{{Column: "qty", Have: "text", Want: "double precision"}}, ColumnTypeMismatches(desired, &actual))
	require.Empty(t, ColumnTypeMismatches(desired, nil))
}

func TestOnlyDroppingAGuaranteeIsDestructive(t *testing.T) {
	for kind, want := range map[DdlOpKind]bool{
		OpDropForeignKey: true, OpDropUnique: true, OpDropSyncTrigger: true,
		OpDropIndexConcurrently: false, OpAddColumn: false, OpCreateIndexConcurrently: false,
	} {
		require.Equal(t, want, DdlOp{Kind: kind}.IsDestructive(), "kind %d", kind)
	}
}
