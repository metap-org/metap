package bootstrap

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

type fakePolicies struct{ rows []permission.PolicyRow }

func (f *fakePolicies) ListPolicies(_ context.Context, _ uuid.UUID, entity string, _ bool) ([]permission.PolicyRow, error) {
	var out []permission.PolicyRow
	for _, r := range f.rows {
		if r.Entity == entity {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakePolicies) CreatePolicy(_ context.Context, _ uuid.UUID, entity, action string, roles []string, hasRoles bool,
	cond *permission.PolicyCondition, _ *uuid.UUID, field string, _ *permission.PolicySubject, effect permission.PolicyEffect) (permission.PolicyRow, error) {
	r := permission.PolicyRow{ID: uuid.New(), Entity: entity, Action: action, Roles: roles, HasRoles: hasRoles,
		Condition: cond, Field: field, Subject: "context", Effect: effect}
	f.rows = append(f.rows, r)
	return r, nil
}

type fakeIdentity struct {
	users map[string][]string // email -> roles
}

func (f *fakeIdentity) FindUserByEmail(_ context.Context, _ uuid.UUID, email string) (uuid.UUID, bool, error) {
	_, ok := f.users[strings.ToLower(email)]
	return uuid.New(), ok, nil
}

func (f *fakeIdentity) CreateUser(_ context.Context, _ uuid.UUID, email, _ string) (uuid.UUID, error) {
	f.users[strings.ToLower(email)] = nil
	return uuid.New(), nil
}

func (f *fakeIdentity) AssignRole(_ context.Context, _, _ uuid.UUID, _ string) error { return nil }

func registry(t *testing.T, names ...string) *metadata.MetadataRegistry {
	t.Helper()
	r := metadata.NewMetadataRegistry()
	for _, n := range names {
		require.NoError(t, r.Register(metadata.EntityDefinition{
			Name: n, Label: n, TableName: "waf." + strings.ReplaceAll(n, ".", "_"),
			Fields: []metadata.EntityField{metadata.NewField("title", "Title", metadata.KindString)},
		}))
	}
	return r
}

func passwords(m map[string]string) PasswordSource {
	return func(_ context.Context, ref string) (string, bool, error) {
		v, ok := m[ref]
		return v, ok, nil
	}
}

func TestApplyExpandsWildcardsAndIsAdditive(t *testing.T) {
	pol := &fakePolicies{}
	deps := Deps{Registry: registry(t, "waf.zones", "waf.rules", "crm.customers"), Policies: pol, Identity: &fakeIdentity{users: map[string][]string{}}}
	b := Bootstrap{
		Roles:  []string{"analyst"},
		Grants: []Grant{{Role: "analyst", Entities: []string{"waf.*"}, Actions: []string{"read", "update"}}},
	}
	tenant := uuid.New()

	res, err := Apply(context.Background(), deps, tenant, b, Options{})
	require.NoError(t, err)
	assert.Equal(t, 4, res.PoliciesCreated) // 2 entities x 2 actions; crm.customers untouched

	// A second run changes nothing.
	res, err = Apply(context.Background(), deps, tenant, b, Options{})
	require.NoError(t, err)
	assert.Equal(t, 0, res.PoliciesCreated)
	assert.Equal(t, 4, res.PoliciesExisting)
	assert.Len(t, pol.rows, 4)

	// An admin-added extra policy survives a re-run untouched.
	pol.rows = append(pol.rows, permission.PolicyRow{ID: uuid.New(), Entity: "waf.zones", Action: "delete", Roles: []string{"analyst"}, HasRoles: true, Subject: "context"})
	_, err = Apply(context.Background(), deps, tenant, b, Options{})
	require.NoError(t, err)
	assert.Len(t, pol.rows, 5)
}

func TestAllActionsExpands(t *testing.T) {
	pol := &fakePolicies{}
	deps := Deps{Registry: registry(t, "waf.zones"), Policies: pol, Identity: &fakeIdentity{users: map[string][]string{}}}
	b := Bootstrap{Roles: []string{"ops"}, Grants: []Grant{{Role: "ops", Entities: []string{"*"}, Actions: []string{AllActions}}}}
	res, err := Apply(context.Background(), deps, uuid.New(), b, Options{})
	require.NoError(t, err)
	assert.Equal(t, 5, res.PoliciesCreated)
}

func TestUnmatchedEntityPatternIsAnError(t *testing.T) {
	deps := Deps{Registry: registry(t, "waf.zones"), Policies: &fakePolicies{}, Identity: &fakeIdentity{users: map[string][]string{}}}
	b := Bootstrap{Roles: []string{"r"}, Grants: []Grant{{Role: "r", Entities: []string{"wfa.*"}, Actions: []string{"read"}}}}
	_, err := Apply(context.Background(), deps, uuid.New(), b, Options{})
	require.ErrorContains(t, err, "matches no registered entity")
}

func TestUsersPasswordsAndProduction(t *testing.T) {
	b := Bootstrap{
		Roles: []string{"analyst"},
		Users: []User{
			{Email: "a@x.io", Roles: []string{"analyst"}, SecretRef: "A_PW"},
			{Email: "b@x.io", Roles: []string{"analyst"}, SecretRef: "B_PW"},
		},
	}
	newDeps := func(pw map[string]string, existing ...string) (Deps, *fakeIdentity) {
		id := &fakeIdentity{users: map[string][]string{}}
		for _, e := range existing {
			id.users[e] = nil
		}
		return Deps{Registry: registry(t), Policies: &fakePolicies{}, Identity: id, Passwords: passwords(pw)}, id
	}

	t.Run("production refuses and writes nothing", func(t *testing.T) {
		d, id := newDeps(map[string]string{"A_PW": "pw-a"})
		_, err := Apply(context.Background(), d, uuid.New(), b, Options{Production: true})
		require.ErrorContains(t, err, "b@x.io")
		assert.Empty(t, id.users, "preflight must abort before creating a.x.io")
	})

	t.Run("dev skips the user without a password", func(t *testing.T) {
		d, id := newDeps(map[string]string{"A_PW": "pw-a"})
		res, err := Apply(context.Background(), d, uuid.New(), b, Options{})
		require.NoError(t, err)
		assert.Equal(t, []string{"a@x.io"}, res.UsersCreated)
		assert.Equal(t, []string{"b@x.io"}, res.UsersSkipped)
		assert.Contains(t, id.users, "a@x.io")
	})

	t.Run("existing user needs no password and is untouched", func(t *testing.T) {
		d, _ := newDeps(map[string]string{}, "a@x.io", "b@x.io")
		res, err := Apply(context.Background(), d, uuid.New(), b, Options{Production: true})
		require.NoError(t, err)
		assert.Len(t, res.UsersExisting, 2)
		assert.Empty(t, res.UsersCreated)
	})
}

func TestValidate(t *testing.T) {
	assert.ErrorContains(t, Bootstrap{Grants: []Grant{{Role: "x", Entities: []string{"*"}, Actions: []string{"read"}}}}.Validate(), "undeclared role")
	assert.ErrorContains(t, Bootstrap{Roles: []string{"x"}, Grants: []Grant{{Role: "x", Entities: []string{"*"}, Actions: []string{"explode"}}}}.Validate(), "unknown action")
	assert.ErrorContains(t, Bootstrap{Users: []User{{Email: "nope"}}}.Validate(), "not an email")
	assert.NoError(t, Bootstrap{Roles: []string{"x"}, Grants: []Grant{{Role: "x", Entities: []string{"a.*"}, Actions: []string{"transition:approve"}}}}.Validate())
}

func TestSubmitCollectMerge(t *testing.T) {
	Submit(func() Bootstrap { return Bootstrap{Roles: []string{"a", "b"}} })
	Submit(func() Bootstrap { return Bootstrap{Roles: []string{"b", "c"}} })
	got := Collect()
	assert.Equal(t, []string{"a", "b", "c"}, got.Roles)
}

func TestLoadFileRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	good := dir + "/good.json"
	require.NoError(t, os.WriteFile(good, []byte(`{"roles":["a"],"grants":[{"role":"a","entities":["x.*"],"actions":["read"]}],"users":[{"email":"a@x.io","roles":["a"],"secretRef":"P"}]}`), 0o600))
	b, err := LoadFile(good)
	require.NoError(t, err)
	assert.NoError(t, b.Validate())
	assert.Equal(t, "P", b.Users[0].SecretRef)

	bad := dir + "/bad.json"
	require.NoError(t, os.WriteFile(bad, []byte(`{"rolez":["a"]}`), 0o600))
	_, err = LoadFile(bad)
	require.Error(t, err)
}
