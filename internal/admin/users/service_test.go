package users

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

type fakeStore struct {
	members   map[uuid.UUID]*identity.Member
	roles     map[uuid.UUID][]string
	scoped    map[uuid.UUID][]permission.ScopedRole
	shared    map[uuid.UUID]bool
	known     []string
	grantErr  error
	mfaOff    []uuid.UUID
	passwords map[uuid.UUID]string
	removed   []uuid.UUID
}

func newStore() *fakeStore {
	return &fakeStore{members: map[uuid.UUID]*identity.Member{}, roles: map[uuid.UUID][]string{},
		scoped: map[uuid.UUID][]permission.ScopedRole{}, shared: map[uuid.UUID]bool{}, passwords: map[uuid.UUID]string{},
		known: []string{"admin", "viewer", "analyst"}}
}

func (f *fakeStore) Members(_ context.Context, _ uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error) {
	var out []identity.Member
	for id, m := range f.members {
		if q.Status != "" && m.Status != q.Status {
			continue
		}
		if len(q.IDs) > 0 && !slices.Contains(q.IDs, id) {
			continue
		}
		out = append(out, *m)
	}
	return out, len(out), nil
}
func (f *fakeStore) Member(_ context.Context, _, id uuid.UUID) (*identity.Member, error) {
	if m, ok := f.members[id]; ok && m.Status != "removed" {
		c := *m
		return &c, nil
	}
	return nil, identity.ErrNotFound
}
func (f *fakeStore) Add(_ context.Context, in identity.NewMember) (*identity.Member, bool, error) {
	for _, m := range f.members {
		if m.Email == in.Email {
			return nil, false, identity.ErrAlreadyMember
		}
	}
	status := in.Status
	if status == "" {
		status = identity.StatusActive
	}
	m := &identity.Member{ID: uuid.New(), IdentityID: uuid.New(), Email: in.Email, DisplayName: in.DisplayName, Status: status, InvitedBy: in.InvitedBy}
	f.members[m.ID] = m
	return m, true, nil
}
func (f *fakeStore) Rename(_ context.Context, _, id uuid.UUID, name string) error {
	m, ok := f.members[id]
	if !ok {
		return identity.ErrNotFound
	}
	m.DisplayName = name
	return nil
}
func (f *fakeStore) SetStatus(_ context.Context, _, id uuid.UUID, status string, _ time.Time) error {
	f.members[id].Status = status
	return nil
}
func (f *fakeStore) Remove(_ context.Context, _, id uuid.UUID, _ time.Time) error {
	f.removed = append(f.removed, id)
	delete(f.members, id)
	return nil
}
func (f *fakeStore) Shared(_ context.Context, _ uuid.UUID, people []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, p := range people {
		out[p] = f.shared[p]
	}
	return out, nil
}
func (f *fakeStore) PendingInvitation(context.Context, uuid.UUID, time.Time) (*identity.Invitation, error) {
	return &identity.Invitation{ExpiresAt: time.Now().Add(time.Hour), TenantName: "Acme"}, nil
}
func (f *fakeStore) NewInvitation(_ context.Context, _ uuid.UUID, _ *identity.Member, _ *uuid.UUID, now time.Time) (string, *identity.Invitation, bool, error) {
	return "tok", &identity.Invitation{ExpiresAt: now.Add(time.Hour), TenantName: "Acme"}, true, nil
}
func (f *fakeStore) CheckPassword(_ context.Context, _ uuid.UUID, pw string, _ uuid.UUID) error {
	if len(pw) < 8 {
		return identity.ErrWeakPassword
	}
	return nil
}
func (f *fakeStore) SetPassword(_ context.Context, p uuid.UUID, pw string, _ time.Time) error {
	f.passwords[p] = pw
	return nil
}
func (f *fakeStore) DisableMFA(_ context.Context, p uuid.UUID) error {
	f.mfaOff = append(f.mfaOff, p)
	return nil
}
func (f *fakeStore) CreateServiceAccount(_ context.Context, _ uuid.UUID, name string, _ *uuid.UUID) (*identity.Member, error) {
	m := &identity.Member{ID: uuid.New(), IdentityID: uuid.New(), DisplayName: name, Status: identity.StatusActive, AuthProvider: identity.ServiceAccountProvider}
	f.members[m.ID] = m
	return m, nil
}
func (f *fakeStore) ServiceAccounts(context.Context, uuid.UUID) ([]identity.Member, error) {
	var out []identity.Member
	for _, m := range f.members {
		if m.AuthProvider == identity.ServiceAccountProvider {
			out = append(out, *m)
		}
	}
	return out, nil
}
func (f *fakeStore) KnownRoles(context.Context, uuid.UUID) ([]string, error) { return f.known, nil }
func (f *fakeStore) RolesOf(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID][]string, map[uuid.UUID][]permission.ScopedRole, error) {
	return f.roles, f.scoped, nil
}
func (f *fakeStore) WithRole(_ context.Context, _ uuid.UUID, role string) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for id, rs := range f.roles {
		if slices.Contains(rs, role) {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *fakeStore) Grant(_ context.Context, _, member uuid.UUID, roles []string, _ *uuid.UUID) error {
	if f.grantErr != nil {
		return f.grantErr
	}
	f.roles[member] = append(f.roles[member], roles...)
	return nil
}
func (f *fakeStore) ChangeRole(_ context.Context, _, member uuid.UUID, role string, assign bool, _ *uuid.UUID) ([]string, error) {
	if assign {
		if !slices.Contains(f.roles[member], role) {
			f.roles[member] = append(f.roles[member], role)
		}
	} else {
		f.roles[member] = slices.DeleteFunc(f.roles[member], func(r string) bool { return r == role })
	}
	return f.roles[member], nil
}
func (f *fakeStore) ChangeScopedRole(_ context.Context, _, member uuid.UUID, role, st, sid string, assign bool, _ *uuid.UUID) ([]permission.ScopedRole, error) {
	sr := permission.ScopedRole{Role: role, ScopeType: st, ScopeID: sid}
	if assign {
		f.scoped[member] = append(f.scoped[member], sr)
	} else {
		f.scoped[member] = slices.DeleteFunc(f.scoped[member], func(x permission.ScopedRole) bool { return x == sr })
	}
	return f.scoped[member], nil
}
func (f *fakeStore) RevokeAll(_ context.Context, _, member uuid.UUID) error {
	delete(f.roles, member)
	return nil
}
func (f *fakeStore) ScopeMembers(context.Context, uuid.UUID, string, string) ([]rolestore.ScopeMember, error) {
	return nil, nil
}

type fakeOwners struct{ owner uuid.UUID }

func (o fakeOwners) Ensure(context.Context, uuid.UUID) (uuid.UUID, error) { return o.owner, nil }
func (o fakeOwners) Guard(_ context.Context, c kernel.Caller, target uuid.UUID, what string) error {
	if target == o.owner && c.Self != o.owner {
		return kernel.Err(http.StatusForbidden, "owner_protected", "Only the tenant owner can "+what+" the owner.")
	}
	return nil
}

type spy struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *spy) Record(_ context.Context, _ uuid.UUID, e audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *spy) Query(context.Context, uuid.UUID, audit.Query) ([]audit.Row, error) { return nil, nil }
func (a *spy) Search(context.Context, uuid.UUID, audit.SearchQuery) ([]audit.Row, error) {
	return nil, nil
}
func (a *spy) VerifyChain(context.Context, uuid.UUID) (audit.ChainReport, error) {
	return audit.ChainReport{OK: true}, nil
}

type world struct {
	svc                   *Service
	store                 *fakeStore
	audit                 *spy
	tenant                uuid.UUID
	owner, admin2, viewer *identity.Member
	support               bool
}

func newWorld() *world {
	w := &world{store: newStore(), audit: &spy{}, tenant: uuid.New()}
	add := func(email string, roles ...string) *identity.Member {
		m := &identity.Member{ID: uuid.New(), IdentityID: uuid.New(), Email: email, Status: identity.StatusActive, CreatedAt: time.Now()}
		w.store.members[m.ID] = m
		w.store.roles[m.ID] = roles
		return m
	}
	w.owner, w.admin2, w.viewer = add("owner@acme.io", "admin"), add("admin2@acme.io", "admin"), add("viewer@acme.io", "viewer")
	deps := kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now,
		IsSupport: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return w.support, nil }}
	w.svc = New(w.store, fakeOwners{owner: w.owner.ID}, nil, deps)
	return w
}

func (w *world) as(self uuid.UUID, role string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: self.String(), Roles: []string{role}, HasRoles: true})
}

func statusOf(err error) int {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return 0
}

func TestNeedsAdmin(t *testing.T) {
	w := newWorld()
	_, err := w.svc.List(w.as(w.viewer.ID, "viewer"), &ListUsersRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.Get(w.as(w.viewer.ID, "viewer"), &UserIdRequest{Id: w.owner.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
}

func TestListShowsRolesAndOwner(t *testing.T) {
	w := newWorld()
	res, err := w.svc.List(w.as(w.owner.ID, "admin"), &ListUsersRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 3, res.Total)
	for _, u := range res.Users {
		assert.Equal(t, u.Id == w.owner.ID.String(), u.IsOwner, u.Email)
	}
	byRole, err := w.svc.List(w.as(w.owner.ID, "admin"), &ListUsersRequest{Role: "viewer"})
	require.NoError(t, err)
	require.Len(t, byRole.Users, 1)
	assert.Equal(t, "viewer@acme.io", byRole.Users[0].Email)
}

func TestCreateNeedsAPasswordForANewAccountAndKnownRoles(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	_, err := w.svc.Create(ctx, &CreateUserRequest{Email: "n@acme.io"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	assert.Empty(t, w.audit.entries, "the change failed, so nothing is recorded (with a Sink the membership rolls back too)")

	_, err = w.svc.Create(ctx, &CreateUserRequest{Email: "n2@acme.io", Password: "long-enough-pw", Roles: []string{"ghost"}})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "an unknown role")
	_, err = w.svc.Create(ctx, &CreateUserRequest{Email: "n3@acme.io", Password: "short"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the password policy")

	res, err := w.svc.Create(ctx, &CreateUserRequest{Email: "n4@acme.io", Password: "long-enough-pw", Roles: []string{"viewer", "viewer"}})
	require.NoError(t, err)
	assert.True(t, res.CreatedIdentity)
	assert.Equal(t, []string{"viewer"}, res.User.Roles, "duplicates are folded")
	assert.Len(t, w.audit.entries, 1)
}

func TestCreateUndoesTheMembershipWhenRolesFail(t *testing.T) {
	w := newWorld()
	w.store.grantErr = errors.New("tenant database down")
	_, err := w.svc.Create(w.as(w.owner.ID, "admin"), &CreateUserRequest{Email: "n@acme.io", Password: "long-enough-pw", Roles: []string{"viewer"}})
	require.Error(t, err)
	assert.Len(t, w.store.removed, 1, "a retry is not refused as already a member")
	assert.Len(t, w.audit.entries, 2, "the creation and its undoing are both in the trail")
}

func TestDurableActionsAreRefusedToASupportSession(t *testing.T) {
	w := newWorld()
	w.support = true
	ctx := w.as(w.admin2.ID, "admin")
	_, err := w.svc.Create(ctx, &CreateUserRequest{Email: "n@acme.io", Password: "long-enough-pw"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.Invite(ctx, &InviteUserRequest{Email: "n@acme.io"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.CreateServiceAccount(ctx, &CreateServiceAccountRequest{Name: "k"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "admin"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.ResetPassword(ctx, &ResetPasswordRequest{Id: w.viewer.ID.String(), Password: "long-enough-pw"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.ResetMfa(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.List(ctx, &ListUsersRequest{})
	assert.NoError(t, err, "it can still look around")
}

func TestGuardsOnTakingAdminAway(t *testing.T) {
	w := newWorld()
	// Not yourself.
	_, err := w.svc.Disable(w.as(w.admin2.ID, "admin"), &UserIdRequest{Id: w.admin2.ID.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	// Not the owner, by another admin.
	_, err = w.svc.Disable(w.as(w.admin2.ID, "admin"), &UserIdRequest{Id: w.owner.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.RevokeRole(w.as(w.admin2.ID, "admin"), &RoleRequest{Id: w.owner.ID.String(), Role: "admin"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	// Not the last active admin.
	w.store.members[w.admin2.ID].Status = identity.StatusDisabled
	_, err = w.svc.Remove(w.as(w.viewer.ID, "admin"), &UserIdRequest{Id: w.owner.ID.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	assert.Empty(t, w.audit.entries, "refused: nothing changed, nothing recorded")
}

func TestDisableEnableRemove(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	off, err := w.svc.Disable(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, identity.StatusDisabled, off.Status)
	_, err = w.svc.Disable(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	require.NoError(t, err)
	assert.Len(t, w.audit.entries, 1, "disabling a disabled member changes nothing")
	on, err := w.svc.Enable(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, identity.StatusActive, on.Status)
	_, err = w.svc.Remove(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	require.NoError(t, err)
	assert.NotContains(t, w.store.roles, w.viewer.ID, "their roles go with them")
	_, err = w.svc.Get(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
}

func TestRolesAreAuditedOnlyWhenTheySetChange(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	_, err := w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "ghost"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "analyst"})
	require.NoError(t, err)
	assert.Len(t, w.audit.entries, 1)
	_, err = w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "analyst"})
	require.NoError(t, err)
	assert.Len(t, w.audit.entries, 1, "assigning a role they hold records nothing")
}

func TestScopedRoles(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	_, err := w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "analyst", ScopeType: "zone"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a scope needs a type and an id")
	u, err := w.svc.AssignRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: "z1"})
	require.NoError(t, err)
	require.Len(t, u.ScopedRoles, 1)
	assert.Equal(t, []string{"viewer"}, u.Roles, "a scoped role is not a tenant-wide one")
	u, err = w.svc.RevokeRole(ctx, &RoleRequest{Id: w.viewer.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: "z1"})
	require.NoError(t, err)
	assert.Empty(t, u.ScopedRoles)
}

func TestResetPasswordAndMfaRefuseASharedPerson(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	w.store.shared[w.viewer.IdentityID] = true
	_, err := w.svc.ResetPassword(ctx, &ResetPasswordRequest{Id: w.viewer.ID.String(), Password: "long-enough-pw"})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	_, err = w.svc.ResetMfa(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	assert.Empty(t, w.store.passwords)
	assert.Empty(t, w.store.mfaOff)

	w.store.shared[w.viewer.IdentityID] = false
	_, err = w.svc.ResetPassword(ctx, &ResetPasswordRequest{Id: w.viewer.ID.String(), Password: "short"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the policy applies to a reset too")
	_, err = w.svc.ResetPassword(ctx, &ResetPasswordRequest{Id: w.viewer.ID.String(), Password: "long-enough-pw"})
	require.NoError(t, err)
	assert.Equal(t, "long-enough-pw", w.store.passwords[w.viewer.IdentityID])
	_, err = w.svc.ResetMfa(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{w.viewer.IdentityID}, w.store.mfaOff)

	_, err = w.svc.ResetPassword(w.as(w.admin2.ID, "admin"), &ResetPasswordRequest{Id: w.owner.ID.String(), Password: "long-enough-pw"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "only the owner resets the owner")
}

func TestInviteWithoutAMailerReturnsTheLink(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	res, err := w.svc.Invite(ctx, &InviteUserRequest{Email: "new@acme.io", Roles: []string{"viewer"}})
	require.NoError(t, err)
	assert.False(t, res.EmailSent)
	assert.Contains(t, res.AcceptUrl, "tok", "the admin passes the link on")
	assert.Equal(t, []string{"viewer"}, res.User.Roles)
	require.Len(t, w.audit.entries, 1)
	assert.Equal(t, audit.ActionCreate, w.audit.entries[0].Action)

	_, err = w.svc.Invite(ctx, &InviteUserRequest{Email: "new@acme.io"})
	assert.Equal(t, http.StatusConflict, statusOf(err), "already a member")

	// Only an invited member can be re-invited.
	_, err = w.svc.ResendInvitation(ctx, &UserIdRequest{Id: w.viewer.ID.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	invited := res.User.Id
	_, err = w.svc.ResendInvitation(ctx, &UserIdRequest{Id: invited})
	require.NoError(t, err)
}

func TestServiceAccounts(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	sa, err := w.svc.CreateServiceAccount(ctx, &CreateServiceAccountRequest{Name: "ci", Roles: []string{"viewer"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"viewer"}, sa.Roles)
	list, err := w.svc.ListServiceAccounts(ctx, &kernel.Empty{})
	require.NoError(t, err)
	require.Len(t, list.Users, 1)
	assert.EqualValues(t, 1, list.Total)

	w.store.grantErr = errors.New("down")
	_, err = w.svc.CreateServiceAccount(ctx, &CreateServiceAccountRequest{Name: "ci2", Roles: []string{"viewer"}})
	require.Error(t, err)
	assert.Len(t, w.store.removed, 1, "no half-made account")
}

func TestUpdateRecordsOnlyARealChange(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.owner.ID, "admin")
	_, err := w.svc.Update(ctx, &UpdateUserRequest{Id: w.viewer.ID.String(), DisplayName: "Vera"})
	require.NoError(t, err)
	_, err = w.svc.Update(ctx, &UpdateUserRequest{Id: w.viewer.ID.String(), DisplayName: "Vera"})
	require.NoError(t, err)
	assert.Len(t, w.audit.entries, 1)
	_, err = w.svc.Update(ctx, &UpdateUserRequest{Id: uuid.NewString(), DisplayName: "x"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
}
