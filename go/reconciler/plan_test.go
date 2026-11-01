package reconciler

import (
	"strings"
	"testing"
)

func TestPlanRendersSQLForAFreshTableWithoutTouchingADatabase(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["field"] = ColumnSpec{SQLType: "text", Nullable: true, Origin: ColumnOrigin{Kind: OriginFramework}}

	planned := Plan(desired, nil, nil)

	if planned[0].Op.Kind != OpCreateTable {
		t.Fatalf("expected CreateTable, got %+v", planned[0].Op)
	}
	if !strings.HasPrefix(planned[0].SQL[0], "CREATE TABLE IF NOT EXISTS") {
		t.Fatalf("got %q", planned[0].SQL[0])
	}
}

func TestPlanLeavesBackfillSQLEmptyRatherThanFabricatingAStatement(t *testing.T) {
	op := DdlOp{Kind: OpBackfillColumn, BackfillOpID: "op", BackfillColumn: "c", BackfillSourceField: "f", BackfillSQLType: "text"}
	sql := buildSQL("t", op)
	if len(sql) != 0 {
		t.Fatalf("expected empty SQL, got %v", sql)
	}
}
