package roles

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

type fakeStore struct {
	roles    map[string]*rolestore.Role
	grants   map[string][]rolestore.RoleGrant
	holders  map[string][]uuid.UUID
	policies map[string][]rolestore.PolicyRef
}

func newFake() *fakeStore {
	f := &fakeStore{roles: map[string]*rolestore.Role{}, grants: map[string][]rolestore.RoleGrant{},
		holders: map[string][]uuid.UUID{}, policies: map[string][]rolestore.PolicyRef{}}
	f.roles["admin"] = &rolestore.Role{ID: uuid.New(), Name: "admin", Builtin: true}
	return f
}

func (f *fakeStore) get(name string) (rolestore.Role, error) {
	if r, ok := f.roles[name]; ok {
		return *r, nil
	}
	return rolestore.Role{}, rolestore.ErrRoleNotFound
}

func (f *fakeStore) Catalog(_ context.Context, _ uuid.UUID, _ string) ([]rolestore.Role, map[string]int, error) {
	var out []rolestore.Role
	counts := map[string]int{}
	for n, r := range f.roles {
		out = append(out, *r)
		counts[n] = len(f.policies[n])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, counts, nil
}
func (f *fakeStore) Get(_ context.Context, _ uuid.UUID, name string) (rolestore.Role, []rolestore.PolicyRef, error) {
	r, err := f.get(name)
	return r, f.policies[name], err
}
func (f *fakeStore) Role(_ context.Context, _ uuid.UUID, name string) (rolestore.Role, error) {
	return f.get(name)
}
func (f *fakeStore) ActiveHolders(_ context.Context, _ uuid.UUID, names []string) (map[string]int, error) {
	out := map[string]int{}
	for _, n := range names {
		out[n] = len(f.holders[n])
	}
	return out, nil
}
func (f *fakeStore) HolderEmails(_ context.Context, _ uuid.UUID, ids []uuid.UUID, limit int) ([]string, error) {
	var out []string
	for i := range ids {
		if i < limit {
			out = append(out, "member@x.io")
		}
	}
	return out, nil
}
func (f *fakeStore) Create(_ context.Context, _ uuid.UUID, name, label, desc, copyFrom string, _ *uuid.UUID) (rolestore.Role, int, error) {
	if _, ok := f.roles[name]; ok {
		return rolestore.Role{}, 0, rolestore.ErrRoleExists
	}
	var copied []rolestore.RoleGrant
	if copyFrom != "" {
		if _, ok := f.roles[copyFrom]; !ok {
			return rolestore.Role{}, 0, rolestore.ErrRoleNotFound
		}
		copied = f.grants[copyFrom]
	}
	r := &rolestore.Role{ID: uuid.New(), Name: name, Label: label, Description: desc}
	f.roles[name] = r
	f.grants[name] = append([]rolestore.RoleGrant(nil), copied...)
	return *r, len(copied), nil
}
func (f *fakeStore) Update(_ context.Context, _ uuid.UUID, name, label, desc string) (rolestore.Role, rolestore.Role, error) {
	r, ok := f.roles[name]
	if !ok {
		return rolestore.Role{}, rolestore.Role{}, rolestore.ErrRoleNotFound
	}
	before := *r
	r.Label, r.Description = label, desc
	return before, *r, nil
}
func (f *fakeStore) Usage(_ context.Context, _ uuid.UUID, name string) (rolestore.Role, []uuid.UUID, []rolestore.PolicyRef, error) {
	r, err := f.get(name)
	return r, f.holders[name], f.policies[name], err
}
func (f *fakeStore) Delete(_ context.Context, _ uuid.UUID, name string) error {
	if _, ok := f.roles[name]; !ok {
		return rolestore.ErrRoleNotFound
	}
	delete(f.roles, name)
	return nil
}
func (f *fakeStore) Permissions(_ context.Context, _ uuid.UUID, name string) ([]rolestore.RoleGrant, []rolestore.AdvancedPolicy, error) {
	if _, err := f.get(name); err != nil {
		return nil, nil, err
	}
	return f.grants[name], nil, nil
}
func (f *fakeStore) ReplaceGrants(_ context.Context, _ uuid.UUID, name string, grants []rolestore.RoleGrant, _ *uuid.UUID) (rolestore.Role, []rolestore.RoleGrant, error) {
	r, err := f.get(name)
	if err != nil {
		return r, nil, err
	}
	before := f.grants[name]
	f.grants[name] = grants
	return r, before, nil
}

type auditSpy struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *auditSpy) Record(_ context.Context, _ uuid.UUID, e audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *auditSpy) Query(context.Context, uuid.UUID, audit.Query) ([]audit.Row, error) {
	return nil, nil
}
func (a *auditSpy) Search(context.Context, uuid.UUID, audit.SearchQuery) ([]audit.Row, error) {
	return nil, nil
}
func (a *auditSpy) VerifyChain(context.Context, uuid.UUID) (audit.ChainReport, error) {
	return audit.ChainReport{OK: true}, nil
}

type world struct {
	svc    *Service
	store  *fakeStore
	audit  *auditSpy
	tenant uuid.UUID
}

func newWorld() *world {
	w := &world{store: newFake(), audit: &auditSpy{}, tenant: uuid.New()}
	w.svc = New(w.store, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now})
	return w
}

func (w *world) admin() context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true})
}

func statusOf(err error) int {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return 0
}

func TestRolesNeedAdmin(t *testing.T) {
	w := newWorld()
	viewer := kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: uuid.NewString(), Roles: []string{"viewer"}, HasRoles: true})
	_, err := w.svc.List(viewer, &ListRolesRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.SetPermissions(viewer, &SetRolePermissionsRequest{Role: "x"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
}

func TestCreateUpdateDeleteAreAudited(t *testing.T) {
	w := newWorld()
	ctx := w.admin()
	_, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "Bad Name"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))

	r, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst", Label: "Analyst"})
	require.NoError(t, err)
	assert.Equal(t, "analyst", r.Name)
	_, err = w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst"})
	assert.Equal(t, http.StatusConflict, statusOf(err))

	up, err := w.svc.Update(ctx, &UpdateRoleRequest{Name: "analyst", Label: "SOC"})
	require.NoError(t, err)
	assert.Equal(t, "SOC", up.Label)

	_, err = w.svc.Delete(ctx, &RoleNameRequest{Name: "analyst"})
	require.NoError(t, err)
	_, err = w.svc.Get(ctx, &RoleNameRequest{Name: "analyst"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	assert.Len(t, w.audit.entries, 3, "create, update, delete")
}

func TestDeleteRefusedWhileInUse(t *testing.T) {
	w := newWorld()
	ctx := w.admin()
	_, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst"})
	require.NoError(t, err)
	w.store.holders["analyst"] = []uuid.UUID{uuid.New(), uuid.New()}
	w.store.policies["analyst"] = []rolestore.PolicyRef{{Entity: "waf.zones", Action: "read"}}
	_, err = w.svc.Delete(ctx, &RoleNameRequest{Name: "analyst"})
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusConflict, se.Status)
	assert.Len(t, se.FieldErrors["members"], 2)
	assert.Equal(t, []string{"waf.zones read"}, se.FieldErrors["policies"])
	assert.Contains(t, w.store.roles, "analyst", "nothing deleted")

	_, err = w.svc.Delete(ctx, &RoleNameRequest{Name: "admin"})
	assert.Equal(t, http.StatusConflict, statusOf(err), "built in")
}

func TestSetPermissionsAuditsOnlyRealChanges(t *testing.T) {
	w := newWorld()
	ctx := w.admin()
	_, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst"})
	require.NoError(t, err)
	w.audit.entries = nil

	set := &SetRolePermissionsRequest{Role: "analyst", Grants: []*RoleGrant{{Entity: "waf.zones", Action: "read"}}}
	out, err := w.svc.SetPermissions(ctx, set)
	require.NoError(t, err)
	assert.Len(t, out.Grants, 1)
	require.Len(t, w.audit.entries, 1)
	assert.Contains(t, w.audit.entries[0].Diff, "granted")

	_, err = w.svc.SetPermissions(ctx, set)
	require.NoError(t, err)
	assert.Len(t, w.audit.entries, 1, "an identical set changes nothing and records nothing")

	_, err = w.svc.SetPermissions(ctx, &SetRolePermissionsRequest{Role: "ghost"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = w.svc.SetPermissions(ctx, &SetRolePermissionsRequest{Role: "admin"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}

func TestCreateCopiesPermissions(t *testing.T) {
	w := newWorld()
	ctx := w.admin()
	_, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst"})
	require.NoError(t, err)
	_, err = w.svc.SetPermissions(ctx, &SetRolePermissionsRequest{Role: "analyst", Grants: []*RoleGrant{{Entity: "waf.zones", Action: "read"}}})
	require.NoError(t, err)
	two, err := w.svc.Create(ctx, &CreateRoleRequest{Name: "analyst_two", CopyFrom: "analyst"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, two.PolicyCount)
	_, err = w.svc.Create(ctx, &CreateRoleRequest{Name: "x", CopyFrom: "ghost"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
}
