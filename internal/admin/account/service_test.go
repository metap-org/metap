package account

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type fakeStore struct {
	members map[uuid.UUID]*identity.Member
	tenants map[uuid.UUID][]identity.Membership
	noIdent bool
}

func (f *fakeStore) Member(_ context.Context, _, id uuid.UUID) (*identity.Member, error) {
	if m, ok := f.members[id]; ok {
		return m, nil
	}
	return nil, identity.ErrNotFound
}
func (f *fakeStore) Rename(_ context.Context, _, id uuid.UUID, name string) error {
	m, ok := f.members[id]
	if !ok {
		return identity.ErrNotFound
	}
	m.DisplayName = name
	return nil
}
func (f *fakeStore) IdentityOf(_ context.Context, _, member uuid.UUID) (uuid.UUID, error) {
	if f.noIdent {
		return uuid.Nil, identity.ErrNotFound
	}
	return f.members[member].IdentityID, nil
}
func (f *fakeStore) Memberships(_ context.Context, person uuid.UUID) ([]identity.Membership, error) {
	return f.tenants[person], nil
}
func (f *fakeStore) Members(_ context.Context, _ uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error) {
	var out []identity.Member
	for _, m := range f.members {
		if m.Status == q.Status {
			out = append(out, *m)
		}
	}
	end := min(q.Offset+q.Limit, len(out))
	if q.Offset >= len(out) {
		return nil, len(out), nil
	}
	return out[q.Offset:end], len(out), nil
}

type world struct {
	svc          *Service
	store        *fakeStore
	tenant, self uuid.UUID
}

func newWorld() *world {
	w := &world{store: &fakeStore{members: map[uuid.UUID]*identity.Member{}, tenants: map[uuid.UUID][]identity.Membership{}},
		tenant: uuid.New(), self: uuid.New()}
	w.store.members[w.self] = &identity.Member{ID: w.self, IdentityID: uuid.New(), Email: "me@acme.io", DisplayName: "Me", Status: identity.StatusActive}
	w.svc = New(w.store, kernel.Deps{NowFor: time.Now})
	return w
}

func (w *world) ctx() context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: w.self.String(), Roles: []string{"viewer"}, HasRoles: true})
}

func TestAnyoneSignedInMaySeeAndRenameThemselves(t *testing.T) {
	w := newWorld()
	me, err := w.svc.Get(w.ctx(), &kernel.Empty{})
	require.NoError(t, err)
	assert.Equal(t, "me@acme.io", me.Email)
	assert.Equal(t, []string{"viewer"}, me.Roles)

	me, err = w.svc.UpdateProfile(w.ctx(), &UpdateProfileRequest{DisplayName: "Mia"})
	require.NoError(t, err)
	assert.Equal(t, "Mia", me.DisplayName)

	_, err = w.svc.Get(context.Background(), &kernel.Empty{})
	var se *crud.ServiceError
	require.True(t, errors.As(err, &se))
	assert.EqualValues(t, http.StatusUnauthorized, se.Status, "no caller")
}

func TestASubjectWithoutAMemberRowStillGetsAnswer(t *testing.T) {
	w := newWorld()
	delete(w.store.members, w.self)
	me, err := w.svc.Get(w.ctx(), &kernel.Empty{})
	require.NoError(t, err, "a token for something that is not a person (a service) has no row")
	assert.Empty(t, me.Email)
	assert.Equal(t, w.self.String(), me.UserId)
}

func TestMyTenantsMarksTheCurrentOne(t *testing.T) {
	w := newWorld()
	person := w.store.members[w.self].IdentityID
	other := uuid.New()
	w.store.tenants[person] = []identity.Membership{{TenantID: w.tenant, TenantName: "Acme"}, {TenantID: other, TenantName: "Globex"}}
	res, err := w.svc.ListMyTenants(w.ctx(), &kernel.Empty{})
	require.NoError(t, err)
	require.Len(t, res.Tenants, 2)
	for _, tn := range res.Tenants {
		assert.Equal(t, tn.TenantId == w.tenant.String(), tn.Current)
	}

	w.store.noIdent = true
	res, err = w.svc.ListMyTenants(w.ctx(), &kernel.Empty{})
	require.NoError(t, err)
	assert.Empty(t, res.Tenants, "no person behind the subject: no tenants, not an error")
}

func TestTenantUsersPagesAndHonoursTheLimit(t *testing.T) {
	w := newWorld()
	for i := 0; i < identity.MaxListLimit+10; i++ {
		id := uuid.New()
		w.store.members[id] = &identity.Member{ID: id, Email: "u@acme.io", Status: identity.StatusActive}
	}
	all, err := w.svc.ListTenantUsers(w.ctx(), &ListTenantUsersRequest{})
	require.NoError(t, err)
	assert.Len(t, all.Users, identity.MaxListLimit+11, "every page is walked")
	few, err := w.svc.ListTenantUsers(w.ctx(), &ListTenantUsersRequest{Limit: 5})
	require.NoError(t, err)
	assert.Len(t, few.Users, 5)
}
