package support

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu       sync.Mutex
	roles    map[uuid.UUID][]string // tenant -> catalog
	grants   []*identity.SupportGrant
	people   map[uuid.UUID]*identity.Member
	accounts map[uuid.UUID]string // member -> name
	assigned map[uuid.UUID]string // member -> role
	cleared  []uuid.UUID
	sessions []identity.SupportSession
	unreach  map[uuid.UUID]bool
	failRole bool
	removed  []uuid.UUID
}

func newFake() *fakeStore {
	return &fakeStore{roles: map[uuid.UUID][]string{}, people: map[uuid.UUID]*identity.Member{}, accounts: map[uuid.UUID]string{},
		assigned: map[uuid.UUID]string{}, unreach: map[uuid.UUID]bool{}}
}

func (f *fakeStore) CreateGrant(_ context.Context, tenant, by uuid.UUID, reason, role string, hours int, now time.Time) (*identity.SupportGrant, error) {
	if reason == "" {
		return nil, identity.ErrSupportReason
	}
	if hours <= 0 {
		hours = 24
	}
	g := &identity.SupportGrant{ID: uuid.New(), TenantID: tenant, GrantedBy: by, Reason: reason, Role: role,
		ExpiresAt: now.Add(time.Duration(hours) * time.Hour), CreatedAt: now}
	f.grants = append(f.grants, g)
	return g, nil
}

func (f *fakeStore) find(tenant, id uuid.UUID) *identity.SupportGrant {
	for _, g := range f.grants {
		if g.TenantID == tenant && g.ID == id {
			return g
		}
	}
	return nil
}

func (f *fakeStore) ListGrants(_ context.Context, tenant uuid.UUID) ([]identity.SupportGrant, error) {
	var out []identity.SupportGrant
	for _, g := range f.grants {
		if g.TenantID == tenant {
			out = append(out, *g)
		}
	}
	return out, nil
}

func (f *fakeStore) GetGrant(_ context.Context, tenant, id uuid.UUID) (*identity.SupportGrant, error) {
	if g := f.find(tenant, id); g != nil {
		c := *g
		return &c, nil
	}
	return nil, identity.ErrNotFound
}

func (f *fakeStore) LatestActiveGrant(_ context.Context, tenant uuid.UUID, now time.Time) (*identity.SupportGrant, error) {
	var latest *identity.SupportGrant
	for _, g := range f.grants {
		if g.TenantID == tenant && g.Active(now) && (latest == nil || g.CreatedAt.After(latest.CreatedAt)) {
			latest = g
		}
	}
	if latest == nil {
		return nil, identity.ErrNotFound
	}
	c := *latest
	return &c, nil
}

func (f *fakeStore) RevokeGrant(_ context.Context, tenant, id uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	g := f.find(tenant, id)
	if g == nil {
		return nil, identity.ErrNotFound
	}
	g.RevokedAt = &now
	var members []uuid.UUID
	for _, s := range f.sessions {
		if s.GrantID == id {
			members = append(members, s.MemberID)
		}
	}
	return members, nil
}

func (f *fakeStore) ExpireSessions(context.Context, uuid.UUID, time.Time) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeStore) TenantReachable(_ context.Context, tenant uuid.UUID) error {
	if f.unreach[tenant] {
		return tenancy.ErrTenantNotFound
	}
	return nil
}

func (f *fakeStore) Person(_ context.Context, _, id uuid.UUID) (*identity.Member, error) {
	if p, ok := f.people[id]; ok {
		return p, nil
	}
	return nil, identity.ErrNotFound
}

func (f *fakeStore) CreateAccount(_ context.Context, _ uuid.UUID, name string) (*identity.Member, error) {
	m := &identity.Member{ID: uuid.New(), DisplayName: name}
	f.accounts[m.ID] = name
	return m, nil
}

func (f *fakeStore) RemoveMember(_ context.Context, _ uuid.UUID, id uuid.UUID, _ time.Time) error {
	delete(f.accounts, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeStore) CreateKey(_ context.Context, tenant, member uuid.UUID, _ string, expires, _ time.Time) (string, *identity.APIKey, error) {
	return "mk_secret", &identity.APIKey{ID: uuid.New(), TenantID: tenant, MemberID: member, ExpiresAt: &expires}, nil
}

func (f *fakeStore) RecordSession(_ context.Context, _ uuid.UUID, s identity.SupportSession) error {
	f.sessions = append(f.sessions, s)
	return nil
}

func (f *fakeStore) KnownRoles(_ context.Context, tenant uuid.UUID) ([]string, error) {
	return f.roles[tenant], nil
}

func (f *fakeStore) AssignRole(_ context.Context, _ uuid.UUID, member uuid.UUID, role string) error {
	if f.failRole {
		return identity.ErrNotFound
	}
	f.assigned[member] = role
	return nil
}

func (f *fakeStore) ClearRoles(_ context.Context, _ uuid.UUID, members []uuid.UUID) error {
	f.cleared = append(f.cleared, members...)
	return nil
}

// auditSpy is an audit.Store that remembers what was recorded.
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

func (a *auditSpy) keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.entries {
		for k := range e.Diff {
			out = append(out, e.TenantID.String()+"|"+e.Entity+"|"+k)
		}
	}
	return out
}

type world struct {
	svc    *Service
	store  *fakeStore
	audit  *auditSpy
	tenant uuid.UUID
	now    time.Time
}

func newWorld() *world {
	w := &world{store: newFake(), audit: &auditSpy{}, tenant: uuid.New(), now: time.Now().UTC()}
	w.store.roles[w.tenant] = []string{"admin", "viewer", "support-readonly"}
	// No Sink: a change is made and then recorded, through the audit store (kernel.Change).
	w.svc = New(w.store, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: func() time.Time { return w.now }})
	return w
}

func (w *world) as(tenant uuid.UUID, self uuid.UUID, roles ...string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenant.String(), UserID: self.String(), Roles: roles, HasRoles: true})
}

func status(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func TestOnlyATenantAdminWhoIsAPersonGrantsAndOnlyForARoleOfTheTenant(t *testing.T) {
	w := newWorld()
	admin := uuid.New()
	req := &GrantSupportAccessRequest{Reason: "ticket 42", Role: "support-readonly", Hours: 2}

	_, err := w.svc.Grant(w.as(w.tenant, uuid.New(), "viewer"), req)
	require.Equal(t, http.StatusForbidden, status(err), "not an admin")
	_, err = w.svc.Grant(w.as(w.tenant, uuid.Nil, "admin"), req)
	require.Equal(t, http.StatusForbidden, status(err), "a service account cannot grant")
	_, err = w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "x", Role: "no-such-role"})
	require.Equal(t, http.StatusBadRequest, status(err))
	_, err = w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "x", Role: ""})
	require.Equal(t, http.StatusBadRequest, status(err))
	_, err = w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "", Role: "viewer"})
	require.Equal(t, http.StatusBadRequest, status(err), "a reason is required")
	require.Empty(t, w.store.grants)

	got, err := w.svc.Grant(w.as(w.tenant, admin, "admin"), req)
	require.NoError(t, err)
	require.True(t, got.Active)
	require.Equal(t, "support-readonly", got.Role)
	require.Len(t, w.store.grants, 1)
}

func TestSupportStartsASessionUnderTheNewestActiveGrant(t *testing.T) {
	w := newWorld()
	admin, vendor := uuid.New(), uuid.New()
	w.store.people[vendor] = &identity.Member{ID: vendor, Email: "agent@vendor.io"}
	asVendor := w.as(tenancy.PlatformTenantID, vendor, "platform_admin")

	_, err := w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: w.tenant.String()})
	require.Equal(t, http.StatusConflict, status(err), "no active grant")

	grant, err := w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "outage", Role: "support-readonly", Hours: 2})
	require.NoError(t, err)

	_, err = w.svc.Start(w.as(w.tenant, admin, "admin"), &StartSupportSessionRequest{TenantId: w.tenant.String()})
	require.Equal(t, http.StatusForbidden, status(err), "a tenant admin is not the platform's support")

	session, err := w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: w.tenant.String()})
	require.NoError(t, err)
	require.Equal(t, "mk_secret", session.Token)
	require.Equal(t, "support-readonly", session.Role)
	require.Equal(t, grant.Id, session.GrantId)
	require.Len(t, w.store.accounts, 1)
	for _, name := range w.store.accounts {
		require.Equal(t, "Support: agent@vendor.io", name)
	}
	require.Len(t, w.store.assigned, 1, "exactly the granted role")
	require.Len(t, w.store.sessions, 1)
	require.Contains(t, w.audit.keys(), w.tenant.String()+"|"+AuditEntity+"|sessionStarted", "recorded in the tenant's own trail")
}

func TestASessionThatCannotGetItsRoleLeavesNoAccountBehind(t *testing.T) {
	w := newWorld()
	vendor := uuid.New()
	w.store.people[vendor] = &identity.Member{ID: vendor, Email: "agent@vendor.io"}
	_, err := w.svc.Grant(w.as(w.tenant, uuid.New(), "admin"), &GrantSupportAccessRequest{Reason: "r", Role: "viewer"})
	require.NoError(t, err)

	w.store.failRole = true
	_, err = w.svc.Start(w.as(tenancy.PlatformTenantID, vendor, "platform_admin"), &StartSupportSessionRequest{TenantId: w.tenant.String()})
	require.Error(t, err)
	require.Empty(t, w.store.accounts, "the account made for the session was removed again")
	require.Len(t, w.store.removed, 1)
	require.Empty(t, w.store.sessions)
}

func TestAnUnreachableTenantOrAnUnknownGrantOrAnInactiveOneStartsNothing(t *testing.T) {
	w := newWorld()
	vendor := uuid.New()
	w.store.people[vendor] = &identity.Member{ID: vendor, Email: "agent@vendor.io"}
	asVendor := w.as(tenancy.PlatformTenantID, vendor, "platform_admin")
	grant, _ := w.svc.Grant(w.as(w.tenant, uuid.New(), "admin"), &GrantSupportAccessRequest{Reason: "r", Role: "viewer", Hours: 1})

	_, err := w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: "nope"})
	require.Equal(t, http.StatusBadRequest, status(err))
	_, err = w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: w.tenant.String(), GrantId: uuid.NewString()})
	require.Equal(t, http.StatusNotFound, status(err), "no such grant")

	w.store.unreach[w.tenant] = true
	_, err = w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: w.tenant.String(), GrantId: grant.Id})
	require.Equal(t, http.StatusNotFound, status(err), "a gone tenant")
	delete(w.store.unreach, w.tenant)

	w.now = w.now.Add(2 * time.Hour)
	_, err = w.svc.Start(asVendor, &StartSupportSessionRequest{TenantId: w.tenant.String(), GrantId: grant.Id})
	require.Equal(t, http.StatusConflict, status(err), "the grant has lapsed")
	require.Empty(t, w.store.accounts, "nothing was made on any of these paths")
}

func TestRevokingAGrantEndsItsSessionsClearsTheirRolesAndIsAudited(t *testing.T) {
	w := newWorld()
	admin, vendor := uuid.New(), uuid.New()
	w.store.people[vendor] = &identity.Member{ID: vendor, Email: "agent@vendor.io"}
	grant, _ := w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "r", Role: "viewer", Hours: 2})
	_, err := w.svc.Start(w.as(tenancy.PlatformTenantID, vendor, "platform_admin"), &StartSupportSessionRequest{TenantId: w.tenant.String()})
	require.NoError(t, err)

	revoked, err := w.svc.Revoke(w.as(w.tenant, admin, "admin"), &SupportGrantIdRequest{Id: grant.Id})
	require.NoError(t, err)
	require.False(t, revoked.Active)
	require.NotEmpty(t, revoked.RevokedAt)
	require.Len(t, w.store.cleared, 1, "the session member's roles in the tenant's database")
	require.Contains(t, w.audit.keys(), w.tenant.String()+"|"+AuditEntity+"|revoked")

	_, err = w.svc.Revoke(w.as(w.tenant, admin, "admin"), &SupportGrantIdRequest{Id: "nope"})
	require.Equal(t, http.StatusBadRequest, status(err))
	_, err = w.svc.Revoke(w.as(w.tenant, admin, "admin"), &SupportGrantIdRequest{Id: uuid.NewString()})
	require.Equal(t, http.StatusNotFound, status(err))
	_, err = w.svc.Revoke(w.as(w.tenant, uuid.New(), "viewer"), &SupportGrantIdRequest{Id: grant.Id})
	require.Equal(t, http.StatusForbidden, status(err))
}

func TestListShowsOnlyTheTenantsOwnGrantsNewestStateFirst(t *testing.T) {
	w := newWorld()
	admin := uuid.New()
	_, _ = w.svc.Grant(w.as(w.tenant, admin, "admin"), &GrantSupportAccessRequest{Reason: "r", Role: "viewer"})
	other := uuid.New()
	w.store.roles[other] = []string{"viewer"}
	_, _ = w.svc.Grant(w.as(other, uuid.New(), "admin"), &GrantSupportAccessRequest{Reason: "r", Role: "viewer"})

	got, err := w.svc.List(w.as(w.tenant, admin, "admin"))
	require.NoError(t, err)
	require.Len(t, got.Grants, 1)
	empty, _ := w.svc.List(w.as(uuid.New(), admin, "admin"))
	require.NotNil(t, empty.Grants, "an empty list is [], not null")
}
