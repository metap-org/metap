package reconciler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanRendersSQLForAFreshTableWithoutTouchingADatabase(t *testing.T) {
	desired := EmptySchema("t")
	desired.Columns["field"] = ColumnSpec{SQLType: "text", Nullable: true, Origin: ColumnOrigin{Kind: OriginFramework}}

	planned := Plan(desired, nil, nil)

	require.Equal(t, OpCreateTable, planned[0].Op.Kind, "expected CreateTable, got %+v", planned[0].Op)
	require.True(t, strings.HasPrefix(planned[0].SQL[0], "CREATE TABLE IF NOT EXISTS"), "got %q", planned[0].SQL[0])
}

func TestPlanLeavesBackfillSQLEmptyRatherThanFabricatingAStatement(t *testing.T) {
	op := DdlOp{Kind: OpBackfillColumn, BackfillOpID: "op", BackfillColumn: "c", BackfillSourceField: "f", BackfillSQLType: "text"}
	sql := buildSQL("t", op)
	require.Len(t, sql, 0, "expected empty SQL, got %v", sql)
}
