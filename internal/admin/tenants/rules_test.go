package tenants

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/tenancy"
)

func TestNewTenant(t *testing.T) {
	name, tier, err := newTenant("  Acme  ", "")
	require.NoError(t, err)
	assert.Equal(t, "Acme", name)
	assert.Equal(t, defaultTier, tier)

	_, _, err = newTenant("   ", "pro")
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusBadRequest, se.Status)
	assert.Contains(t, se.FieldErrors, "name")

	long := make([]byte, maxTierLen+1)
	for i := range long {
		long[i] = 'x'
	}
	_, _, err = newTenant("X", string(long))
	require.ErrorAs(t, err, &se)
	assert.Contains(t, se.FieldErrors, "tier")
}

func TestPatch(t *testing.T) {
	before := &tenancy.TenantDetail{Name: "Old"}
	before.Tier = "trial"
	nm, pro, blank := "New", "pro", " "

	n, tr, diff, err := patch(before, &nm, &pro)
	require.NoError(t, err)
	assert.Equal(t, "New", *n)
	assert.Equal(t, "pro", *tr)
	assert.Equal(t, [2]any{"Old", "New"}, diff["name"])
	assert.Equal(t, [2]any{"trial", "pro"}, diff["tier"])

	_, _, diff, err = patch(before, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, diff)

	_, _, _, err = patch(before, &blank, nil)
	assert.Error(t, err)
	_, _, _, err = patch(before, nil, &blank)
	assert.Error(t, err)
}

func TestView(t *testing.T) {
	d := tenancy.TenantDetail{Name: "Acme"}
	d.ID = uuid.New()
	d.Tier = "pro"
	d.Strategy = tenancy.TenantStrategy{Kind: tenancy.StrategyDedicatedDb}
	p := "waf"
	d.Product = &p
	v := view(d, 3)
	assert.Equal(t, "dedicated_db", v.Strategy)
	assert.Equal(t, "waf", v.Product)
	assert.EqualValues(t, 3, v.MemberCount)
	assert.Equal(t, d.ID.String(), v.Id)
}
