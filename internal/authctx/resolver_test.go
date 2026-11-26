package authctx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
)

type fakeStore struct {
	sessionErr   error
	sessionIDErr error
	key          *identity.APIKey
	keyErr       error
	held         Held
	rolesErr     error
	attrs        map[string]any
	attrsErr     error

	sessionChecks []*time.Time
	roleReads     int
	attrReads     int
}

func (f *fakeStore) CheckSession(_ context.Context, _, _ uuid.UUID, issuedAt *time.Time) error {
	f.sessionChecks = append(f.sessionChecks, issuedAt)
	return f.sessionErr
}
func (f *fakeStore) CheckSessionID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	return f.sessionIDErr
}
func (f *fakeStore) ResolveAPIKey(context.Context, string, time.Time) (*identity.APIKey, error) {
	return f.key, f.keyErr
}
func (f *fakeStore) Roles(context.Context, uuid.UUID, uuid.UUID) (Held, error) {
	f.roleReads++
	return f.held, f.rolesErr
}
func (f *fakeStore) ContextAttributes(context.Context, uuid.UUID, string, uuid.UUID) (map[string]any, error) {
	f.attrReads++
	return f.attrs, f.attrsErr
}

func TestContextCarriesTheRolesAndChecksTheSessionFirst(t *testing.T) {
	st := &fakeStore{held: Held{Roles: []string{"viewer"}, Scoped: []permission.ScopedRole{{Role: "analyst", ScopeType: "zone", ScopeID: "z1"}}}}
	r := &Resolver{Store: st}
	tenant, user := uuid.New(), uuid.New()
	iat := time.Now()
	rc, err := r.Context(context.Background(), tenant, user, "fn", &iat)
	require.NoError(t, err)
	assert.Equal(t, tenant.String(), rc.TenantID)
	assert.Equal(t, user.String(), rc.UserID)
	assert.Equal(t, []string{"viewer"}, rc.Roles)
	assert.True(t, rc.HasRoles)
	assert.Equal(t, "fn", rc.FunctionID)
	require.Len(t, rc.ScopedRoles, 1)
	assert.Empty(t, rc.ForwardedBearerToken)
	assert.Equal(t, []*time.Time{&iat}, st.sessionChecks)
}

func TestARevokedOrDisabledMemberGetsNoContextAndNoRoleRead(t *testing.T) {
	for _, bad := range []error{identity.ErrSessionRevoked, identity.ErrDisabled} {
		st := &fakeStore{sessionErr: bad}
		_, err := (&Resolver{Store: st}).Context(context.Background(), uuid.New(), uuid.New(), "", nil)
		assert.ErrorIs(t, err, bad)
		assert.Zero(t, st.roleReads, "the session is checked before the roles are read")
	}
}

func TestRolesFailureIsAnError(t *testing.T) {
	st := &fakeStore{rolesErr: errors.New("db down")}
	_, err := (&Resolver{Store: st}).Context(context.Background(), uuid.New(), uuid.New(), "", nil)
	assert.Error(t, err, "identity without roles is unusable")
}

func TestRolesAreReusedFromTheCacheButTheSessionIsNeverCached(t *testing.T) {
	st := &fakeStore{held: Held{Roles: []string{"admin"}}}
	r := &Resolver{Store: st, Roles: NewRolesCache(time.Minute)}
	tenant, user := uuid.New(), uuid.New()
	for range 3 {
		_, err := r.Context(context.Background(), tenant, user, "", nil)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, st.roleReads, "roles are read once within the TTL")
	assert.Len(t, st.sessionChecks, 3, "the session is checked on every request")

	r.Roles.Invalidate(tenant, user)
	_, err := r.Context(context.Background(), tenant, user, "", nil)
	require.NoError(t, err)
	assert.Equal(t, 2, st.roleReads, "a change in this process shows at once")
}

func TestEditingOneContextDoesNotChangeTheNext(t *testing.T) {
	st := &fakeStore{held: Held{Roles: []string{"viewer"}}}
	r := &Resolver{Store: st, Roles: NewRolesCache(time.Minute)}
	tenant, user := uuid.New(), uuid.New()
	first, _ := r.Context(context.Background(), tenant, user, "", nil)
	first.Roles[0] = "admin"
	second, _ := r.Context(context.Background(), tenant, user, "", nil)
	assert.Equal(t, []string{"viewer"}, second.Roles, "the cached roles are copied out")
}

func TestContextAttributesAreOffWithoutAnEntity(t *testing.T) {
	st := &fakeStore{attrs: map[string]any{"orgId": "o1"}}
	rc, err := (&Resolver{Store: st, Attributes: NewContextAttributesCache(time.Minute)}).Context(context.Background(), uuid.New(), uuid.New(), "", nil)
	require.NoError(t, err)
	assert.Nil(t, rc.ContextAttributes)
	assert.Zero(t, st.attrReads)
}

func TestAttributesJSON(t *testing.T) {
	assert.Nil(t, attributesJSON(nil))
	out := attributesJSON(map[string]any{"orgId": "o1", "level": 3, "bad": make(chan int)})
	assert.JSONEq(t, `"o1"`, string(out["orgId"]))
	assert.JSONEq(t, `3`, string(out["level"]))
	assert.NotContains(t, out, "bad", "a value that cannot be encoded is left out")
	empty := attributesJSON(map[string]any{})
	assert.NotNil(t, empty, "empty stays empty, not nil")
	assert.Empty(t, empty)
}

func TestAnAPIKeyCountsAsASessionIssuedWhenItWasCreated(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	tenant, member := uuid.New(), uuid.New()
	st := &fakeStore{key: &identity.APIKey{TenantID: tenant, MemberID: member, CreatedAt: created}, held: Held{Roles: []string{"admin"}}}
	rc, err := (&Resolver{Store: st}).APIKey(context.Background(), "mk_x", time.Now())
	require.NoError(t, err)
	assert.Equal(t, member.String(), rc.UserID)
	require.Len(t, st.sessionChecks, 1)
	require.NotNil(t, st.sessionChecks[0])
	assert.True(t, st.sessionChecks[0].Equal(created), "revoking sessions after the key was made kills it")

	st.keyErr = identity.ErrInvalidCredentials
	_, err = (&Resolver{Store: st}).APIKey(context.Background(), "mk_bad", time.Now())
	assert.ErrorIs(t, err, identity.ErrInvalidCredentials)
}

func TestApplySession(t *testing.T) {
	st := &fakeStore{}
	r := &Resolver{Store: st}
	ctx := context.Background()
	rc := &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString()}

	require.NoError(t, r.ApplySession(ctx, rc, nil, time.Now()), "a token with no sid is not a login session")
	assert.Empty(t, rc.SessionID)

	sid := uuid.NewString()
	require.NoError(t, r.ApplySession(ctx, rc, &sid, time.Now()))
	assert.Equal(t, sid, rc.SessionID)

	bad := "not-a-uuid"
	assert.ErrorIs(t, r.ApplySession(ctx, &permission.RequestContext{TenantID: rc.TenantID, UserID: rc.UserID}, &bad, time.Now()), identity.ErrSessionRevoked)
	assert.ErrorIs(t, r.ApplySession(ctx, &permission.RequestContext{TenantID: "x", UserID: rc.UserID}, &sid, time.Now()), identity.ErrSessionRevoked)

	st.sessionIDErr = identity.ErrSessionRevoked
	fresh := &permission.RequestContext{TenantID: rc.TenantID, UserID: rc.UserID}
	assert.ErrorIs(t, r.ApplySession(ctx, fresh, &sid, time.Now()), identity.ErrSessionRevoked)
	assert.Empty(t, fresh.SessionID, "a revoked session is not recorded on the context")
}
