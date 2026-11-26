package policies

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/permission"
)

func TestBasicGrantsListsOnlyWhatTheMatrixOwns(t *testing.T) {
	rows := []permission.PolicyRow{
		{Action: "read", Roles: []string{"viewer"}, HasRoles: true, Subject: "context", Effect: permission.EffectAllow},
		{Action: "update", HasRoles: false, Subject: "context", Effect: permission.EffectAllow},
		{Action: "delete", Roles: []string{"viewer"}, HasRoles: true, Subject: "context", Effect: permission.EffectDeny},                  // deny: advanced
		{Action: "read", Roles: []string{"analyst"}, HasRoles: true, Subject: "record", Effect: permission.EffectAllow},                   // record subject: advanced
		{Action: "read", Roles: []string{"analyst"}, HasRoles: true, Subject: "context", Field: "secret", Effect: permission.EffectAllow}, // field: advanced
	}
	require.Equal(t, []string{"*:update", "viewer:read"}, basicGrants(rows))
}

func TestNewPolicy(t *testing.T) {
	by := optionalUUID("")
	assert.Nil(t, by)
	p, err := newPolicy(&CreatePolicyRequest{Entity: "e", Action: "read", Roles: []string{"a"}}, nil)
	require.NoError(t, err)
	assert.True(t, p.HasRoles)
	assert.Equal(t, permission.SubjectContext, p.Subject)
	assert.Equal(t, permission.EffectAllow, p.Effect)

	rec, deny := "record", "deny"
	p, err = newPolicy(&CreatePolicyRequest{Entity: "e", Action: "read", Subject: &rec, Effect: &deny}, nil)
	require.NoError(t, err)
	assert.False(t, p.HasRoles, "no roles = everyone")
	assert.Equal(t, permission.SubjectRecord, p.Subject)
	assert.Equal(t, permission.EffectDeny, p.Effect)

	_, err = newPolicy(&CreatePolicyRequest{Entity: "e", Action: "read", Condition: []byte(`{"op":`)}, nil)
	assert.Error(t, err)
}

func TestSeedActions(t *testing.T) {
	_, err := seedActions(&SeedDefaultPoliciesRequest{Entity: "e"})
	assert.Error(t, err, "roles are required")
	acts, err := seedActions(&SeedDefaultPoliciesRequest{Entity: "e", Roles: []string{"a"}})
	require.NoError(t, err)
	assert.Equal(t, permission.EntityActionNames(), acts)
	_, err = seedActions(&SeedDefaultPoliciesRequest{Entity: "e", Roles: []string{"a"}, Actions: []string{"fly"}})
	assert.Error(t, err)
}

func TestMatrixGrants(t *testing.T) {
	role := "viewer"
	out, err := matrixGrants([]MatrixGrant{{Role: &role, Action: "read"}, {Action: "update"}})
	require.NoError(t, err)
	assert.True(t, out[0].HasRole)
	assert.False(t, out[1].HasRole, "nil role is everyone")
	_, err = matrixGrants([]MatrixGrant{{Action: "fly"}})
	assert.Error(t, err)
}

func TestFieldsOfGone(t *testing.T) {
	row := permission.PolicyRow{Entity: "e", Action: "read", Effect: permission.EffectAllow, Subject: "context"}
	assert.Equal(t, [2]any{"e", nil}, fieldsOf(row, true)["entity"])
	assert.Equal(t, [2]any{nil, "e"}, fieldsOf(row, false)["entity"])
}
