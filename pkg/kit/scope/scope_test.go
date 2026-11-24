package scope

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var zoneColumns = map[string]string{"zone": "zone_id", "account": "account_id"}

func TestTheZeroScopeCannotBoundARead(t *testing.T) {
	require.ErrorIs(t, Scope{}.Valid(), ErrInvalid)
	_, err := Scope{}.Plan("tenant_id", zoneColumns)
	require.ErrorIs(t, err, ErrInvalid, "an adapter that is handed the zero scope refuses it")
	require.ErrorIs(t, ForTenant("  ").Valid(), ErrInvalid)
	require.NoError(t, ForTenant("t1").Valid())
}

func TestAScopeLimitedOnNothingSeesTheWholeTenant(t *testing.T) {
	plan, err := ForTenant("t1").Plan("tenant_id", zoneColumns)
	require.NoError(t, err)
	require.False(t, plan.None)
	require.Equal(t, []Clause{{Column: "tenant_id", Values: []string{"t1"}}}, plan.Clauses)
	require.True(t, ForTenant("t1").Allows("zone", "anything"))
}

func TestALimitAddsAConditionOnItsColumn(t *testing.T) {
	plan, err := ForTenant("t1").Limit("zone", "z1", "z2").Plan("tenant_id", zoneColumns)
	require.NoError(t, err)
	require.Equal(t, []Clause{
		{Column: "tenant_id", Values: []string{"t1"}},
		{Column: "zone_id", Values: []string{"z1", "z2"}},
	}, plan.Clauses)
}

func TestLimitingTwiceKeepsOnlyWhatBothAllow(t *testing.T) {
	s := ForTenant("t1").Limit("zone", "z1", "z2", "z3").Limit("zone", "z2", "z3", "z9")
	require.True(t, s.Allows("zone", "z2"))
	require.True(t, s.Allows("zone", "z3"))
	require.False(t, s.Allows("zone", "z1"), "narrowed away")
	require.False(t, s.Allows("zone", "z9"), "never widened by the second call")
}

func TestALimitDoesNotChangeTheScopeItWasCalledOn(t *testing.T) {
	base := ForTenant("t1")
	narrowed := base.Limit("zone", "z1")
	require.True(t, base.Allows("zone", "z2"), "the original is unchanged")
	require.False(t, narrowed.Allows("zone", "z2"))
	limits := narrowed.Limits()
	limits["zone"][0] = "tampered"
	require.True(t, narrowed.Allows("zone", "z1"), "Limits is a copy")
}

func TestADimensionWithNothingLeftMatchesNothing(t *testing.T) {
	for name, s := range map[string]Scope{
		"limited to nothing":       ForTenant("t1").Limit("zone"),
		"limited to disjoint sets": ForTenant("t1").Limit("zone", "z1").Limit("zone", "z2"),
	} {
		plan, err := s.Plan("tenant_id", zoneColumns)
		require.NoError(t, err, name)
		require.True(t, plan.None, "%s: an empty limit is closed, not open", name)
		require.False(t, s.Allows("zone", "z1"), name)
	}
}

func TestADimensionTheStoreCannotFilterOnIsAnErrorNotASkippedLimit(t *testing.T) {
	_, err := ForTenant("t1").Limit("region", "eu").Plan("tenant_id", zoneColumns)
	require.Error(t, err)
	require.Contains(t, err.Error(), "region")
	require.False(t, errors.Is(err, ErrInvalid))
}

func TestClausesComeOutInAStableOrder(t *testing.T) {
	s := ForTenant("t1").Limit("zone", "z1").Limit("account", "a1")
	for range 20 {
		plan, err := s.Plan("tenant_id", zoneColumns)
		require.NoError(t, err)
		require.Equal(t, []string{"tenant_id", "account_id", "zone_id"}, []string{plan.Clauses[0].Column, plan.Clauses[1].Column, plan.Clauses[2].Column})
	}
}

func TestAClauseIsRenderedWithParametersNeverWithItsValues(t *testing.T) {
	sql, args := Clause{Column: "zone_id", Values: []string{"z1"}}.SQL("?")
	require.Equal(t, "zone_id = ?", sql)
	require.Equal(t, []any{"z1"}, args)

	sql, args = Clause{Column: "zone_id", Values: []string{"z1", "z2", "z3"}}.SQL("toUUID(?)")
	require.Equal(t, "zone_id IN (toUUID(?), toUUID(?), toUUID(?))", sql)
	require.Equal(t, []any{"z1", "z2", "z3"}, args)

	sql, _ = Clause{Column: "zone_id", Values: []string{"z1'; DROP TABLE events; --"}}.SQL("?")
	require.False(t, strings.Contains(sql, "DROP"), "a value never reaches the statement text")
}

func TestDuplicateValuesAreCollapsed(t *testing.T) {
	plan, err := ForTenant("t1").Limit("zone", "z1", "z1", "z2").Plan("tenant_id", zoneColumns)
	require.NoError(t, err)
	require.Equal(t, []string{"z1", "z2"}, plan.Clauses[1].Values)
}
