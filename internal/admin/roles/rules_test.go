package roles

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"analyst", "a1_b2", "x"} {
		assert.NoError(t, checkName(ok), ok)
	}
	for _, bad := range []string{"", "Analyst", "1abc", "has space", "a-b", "__everyone__", string(make([]byte, 81))} {
		assert.Error(t, checkName(bad), bad)
	}
}

func TestValidGrantAction(t *testing.T) {
	for _, ok := range []string{"read", "create", "update", "delete", "transition:approve", "action:verifyDns"} {
		assert.True(t, validGrantAction(ok), ok)
	}
	for _, bad := range []string{"", "write", "transition:", "transition:1x", "action:a b"} {
		assert.False(t, validGrantAction(bad), bad)
	}
}

func TestCheckGrants(t *testing.T) {
	out, err := checkGrants("analyst", []*RoleGrant{{Entity: " waf.zones ", Action: " read "}})
	require.NoError(t, err)
	assert.Equal(t, []rolestore.RoleGrant{{Entity: "waf.zones", Action: "read"}}, out)

	_, err = checkGrants("admin", nil)
	assert.Error(t, err, "admin has no permissions to set")
	_, err = checkGrants("analyst", []*RoleGrant{{Entity: "", Action: "read"}})
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusBadRequest, se.Status)
	_, err = checkGrants("analyst", []*RoleGrant{{Entity: "waf.zones", Action: "fly"}})
	assert.Error(t, err)
	_, err = checkGrants("analyst", []*RoleGrant{{Entity: "resource:nope", Action: "read"}})
	assert.Error(t, err, "an undeclared resource")
}

func TestGrantDiff(t *testing.T) {
	g := func(e, a string) rolestore.RoleGrant { return rolestore.RoleGrant{Entity: e, Action: a} }
	added, removed := grantDiff([]rolestore.RoleGrant{g("a", "read"), g("b", "read")}, []rolestore.RoleGrant{g("b", "read"), g("c", "update")})
	assert.Equal(t, []string{"c update"}, added)
	assert.Equal(t, []string{"a read"}, removed)
}

func TestInUseError(t *testing.T) {
	policies := make([]rolestore.PolicyRef, 7)
	for i := range policies {
		policies[i] = rolestore.PolicyRef{Entity: "e", Action: "read"}
	}
	err := inUseError("analyst", 8, []string{"a@x.io", "b@x.io"}, policies)
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusConflict, se.Status)
	assert.Equal(t, []string{"a@x.io", "b@x.io", "+6"}, se.FieldErrors["members"])
	assert.Len(t, se.FieldErrors["policies"], usageSampleSize+1)
	assert.Equal(t, "+2", se.FieldErrors["policies"][usageSampleSize])
}

func TestView(t *testing.T) {
	r := rolestore.Role{ID: uuid.New(), Name: "x", Builtin: true}
	v := view(r, 3, 4)
	assert.EqualValues(t, 3, v.UserCount)
	assert.EqualValues(t, 4, v.PolicyCount)
	assert.True(t, v.Builtin)
}
