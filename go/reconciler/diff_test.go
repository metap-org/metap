package reconciler

import "testing"

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
	if ops[0].Kind != OpCreateTable {
		t.Fatalf("expected first op CreateTable, got %+v", ops[0])
	}
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddColumn && o.ColumnName == "id" }); !ok {
		t.Fatal("expected AddColumn id")
	}
	if _, ok := findOp(ops, func(o DdlOp) bool {
		return o.Kind == OpCreateIndexConcurrently && o.IndexName == "idx_hr_employees_dept"
	}); !ok {
		t.Fatal("expected CreateIndexConcurrently idx_hr_employees_dept")
	}
}

func TestMatchingActualProducesNoOps(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["id"] = diffCol("uuid")
	desired.Indexes["idx_t_f"] = diffIdx("(data ->> 'f')::text")
	actual := clonePhysicalSchema(desired)

	if ops := Diff(desired, &actual, nil); len(ops) != 0 {
		t.Fatalf("expected no ops, got %+v", ops)
	}
}

func TestMatchingAfterNormalizationProducesNoOpsEvenWhenRawStringsDiffer(t *testing.T) {
	desired := EmptySchema("t")
	desired.Indexes["idx_t_f"] = diffIdx("(data ->> 'f')::text")
	actual := EmptySchema("t")
	actual.Indexes["idx_t_f"] = diffIdx("((data ->> 'f'::text))")

	if ops := Diff(desired, &actual, nil); len(ops) != 0 {
		t.Fatalf("expected no ops, got %+v", ops)
	}
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
	if dropPos >= createPos {
		t.Fatalf("expected drop before create, got drop=%d create=%d", dropPos, createPos)
	}
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

	if pDropUnique >= pDropIndex {
		t.Fatal("DropUnique must run before DropIndexConcurrently for the same name")
	}
	if pDropUnique >= pCreateIndex {
		t.Fatal("DropUnique must run before the new index is created under the same name")
	}
	if pDropIndex >= pCreateIndex {
		t.Fatal("drop strictly before recreate")
	}
}

func TestMissingColumnNotInDesiredIsNeverDropped(t *testing.T) {
	desired := EmptySchema("t")
	actual := EmptySchema("t")
	actual.Columns["stale"] = diffCol("text")

	if ops := Diff(desired, &actual, nil); len(ops) != 0 {
		t.Fatalf("expected no ops, got %+v", ops)
	}
}

func TestRenameAppliesBeforeColumnDiffSoNoDropAddPairAppears(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["new_name"] = diffCol("text")
	actual := EmptySchema("t")
	actual.Columns["old_name"] = diffCol("text")

	ops := Diff(desired, &actual, [][2]string{{"old_name", "new_name"}})
	if len(ops) != 1 || ops[0].Kind != OpRenameColumn || ops[0].RenameFrom != "old_name" || ops[0].RenameTo != "new_name" {
		t.Fatalf("got %+v", ops)
	}
}

func TestUnbackfilledGeneratedColumnThatAlreadyExistsResumesWithoutReadd(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginGenerated, SourceField: "amount", Backfilled: true}}
	actual := EmptySchema("t")
	actual.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginGenerated, SourceField: "amount", Backfilled: false}}

	ops := Diff(desired, &actual, nil)
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddColumn }); ok {
		t.Fatal("must not re-add an existing column")
	}
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpBackfillColumn }); !ok {
		t.Fatal("must resume the backfill")
	}
}

func TestBackfilledGeneratedColumnProducesNoOps(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["amount"] = ColumnSpec{SQLType: "numeric(18,4)", Nullable: true,
		Origin: ColumnOrigin{Kind: OriginGenerated, SourceField: "amount", Backfilled: true}}
	actual := clonePhysicalSchema(desired)

	if ops := Diff(desired, &actual, nil); len(ops) != 0 {
		t.Fatalf("expected no ops, got %+v", ops)
	}
}

func TestFkAddedNotValidThenValidatedWhenMissing(t *testing.T) {
	desired := EmptySchema("hr_employees")
	desired.ForeignKeys["fk_hr_employees_departmentid"] = FkSpec{
		Column: "departmentId", RefTable: "hr_departments", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true,
	}
	ops := Diff(desired, nil, nil)
	addPos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpAddForeignKeyNotValid })
	validatePos, _ := findOp(ops, func(o DdlOp) bool { return o.Kind == OpValidateForeignKey })
	if addPos >= validatePos {
		t.Fatal("a brand-new FK must be added then validated in the same pass")
	}
}

func TestFkAddedButNotValidatedOnlyValidatesNoDuplicateAdd(t *testing.T) {
	desired := EmptySchema("t")
	desired.ForeignKeys["fk_t_x"] = FkSpec{Column: "x", RefTable: "other", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true}
	actual := clonePhysicalSchema(desired)
	fk := actual.ForeignKeys["fk_t_x"]
	fk.Validated = false
	actual.ForeignKeys["fk_t_x"] = fk

	ops := Diff(desired, &actual, nil)
	if len(ops) != 1 || ops[0].Kind != OpValidateForeignKey || ops[0].FkName != "fk_t_x" {
		t.Fatalf("got %+v", ops)
	}
}

func TestOrphanIndexAndFkAndUniqueAreDropped(t *testing.T) {
	desired := EmptySchema("t")
	actual := EmptySchema("t")
	actual.Indexes["idx_gone"] = diffIdx("x")
	actual.ForeignKeys["fk_gone"] = FkSpec{Column: "x", RefTable: "other", RefColumn: "id", OnDelete: OnDeleteRestrict, Validated: true}
	actual.Uniques["uq_gone"] = UniqueSpec{Columns: []string{"x"}}

	ops := Diff(desired, &actual, nil)
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropIndexConcurrently && o.IndexName == "idx_gone" }); !ok {
		t.Fatal("expected idx_gone dropped")
	}
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropForeignKey && o.FkName == "fk_gone" }); !ok {
		t.Fatal("expected fk_gone dropped")
	}
	if _, ok := findOp(ops, func(o DdlOp) bool { return o.Kind == OpDropUnique && o.UniqueName == "uq_gone" }); !ok {
		t.Fatal("expected uq_gone dropped")
	}
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
	if !(pTable < pCol && pCol < pIdx && pIdx < pFk) {
		t.Fatalf("got table=%d col=%d idx=%d fk=%d", pTable, pCol, pIdx, pFk)
	}
}
