package owner

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

type fakeStore struct {
	owner    uuid.UUID
	members  map[uuid.UUID]*identity.Member
	admins   []uuid.UUID
	recovery *identity.OwnerRecovery
	granted  []uuid.UUID
	name     string
	deleted  bool
	pwOK     bool
	mfa      bool
	mfaOK    bool
}

func (f *fakeStore) OwnerID(context.Context, uuid.UUID) (uuid.UUID, error) { return f.owner, nil }
func (f *fakeStore) SetOwner(_ context.Context, _, m uuid.UUID) error      { f.owner = m; return nil }
func (f *fakeStore) people(ids []uuid.UUID) []identity.Member {
	var out []identity.Member
	for _, id := range ids {
		out = append(out, *f.members[id])
	}
	return out
}
func (f *fakeStore) ActiveAdmins(context.Context, uuid.UUID) ([]identity.Member, error) {
	return f.people(f.admins), nil
}
func (f *fakeStore) ActivePeople(context.Context, uuid.UUID) ([]identity.Member, error) {
	var ids []uuid.UUID
	for id := range f.members {
		ids = append(ids, id)
	}
	return f.people(ids), nil
}
func (f *fakeStore) Member(_ context.Context, _, id uuid.UUID) (*identity.Member, error) {
	if m, ok := f.members[id]; ok {
		return m, nil
	}
	return nil, identity.ErrNotFound
}
func (f *fakeStore) TenantName(context.Context, uuid.UUID) string { return f.name }
func (f *fakeStore) PendingRecovery(context.Context, uuid.UUID) (*identity.OwnerRecovery, error) {
	if f.recovery == nil {
		return nil, identity.ErrNoRecovery
	}
	return f.recovery, nil
}
func (f *fakeStore) RequestRecovery(_ context.Context, _, newOwner, by uuid.UUID, email, reason string, now time.Time) (*identity.OwnerRecovery, error) {
	if f.recovery != nil {
		return nil, identity.ErrRecoveryPending
	}
	f.recovery = &identity.OwnerRecovery{ID: uuid.New(), NewOwnerMember: newOwner, RequestedByEmail: email, Reason: reason,
		EffectiveAt: now.Add(identity.OwnerRecoveryWait), CreatedAt: now}
	return f.recovery, nil
}
func (f *fakeStore) CancelRecovery(context.Context, uuid.UUID, uuid.UUID, time.Time) (*identity.OwnerRecovery, error) {
	r := f.recovery
	if r == nil {
		return nil, identity.ErrNoRecovery
	}
	f.recovery = nil
	return r, nil
}
func (f *fakeStore) CompleteRecovery(_ context.Context, _ uuid.UUID, now time.Time) (*identity.OwnerRecovery, error) {
	r := f.recovery
	f.recovery = nil
	r.CompletedAt = &now
	return r, nil
}
func (f *fakeStore) GrantAdmin(_ context.Context, _, m uuid.UUID) error {
	f.granted = append(f.granted, m)
	return nil
}
func (f *fakeStore) ScheduleDeletion(context.Context, uuid.UUID, time.Time, time.Duration) error {
	if f.deleted {
		return errors.New("already")
	}
	f.deleted = true
	return nil
}
func (f *fakeStore) CancelDeletion(context.Context, uuid.UUID, time.Time) error {
	f.deleted = false
	return nil
}
func (f *fakeStore) CancelToken(context.Context, uuid.UUID, time.Time) (string, error) {
	return "tok", nil
}
func (f *fakeStore) IdentityOf(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (f *fakeStore) VerifyPassword(context.Context, uuid.UUID, string, time.Time) error {
	if f.pwOK {
		return nil
	}
	return identity.ErrInvalidCredentials
}
func (f *fakeStore) MFAEnabled(context.Context, uuid.UUID) (bool, error) { return f.mfa, nil }
func (f *fakeStore) VerifyMFA(context.Context, uuid.UUID, string, time.Time) error {
	if f.mfaOK {
		return nil
	}
	return identity.ErrInvalidCredentials
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
	svc                *Service
	store              *fakeStore
	audit              *spy
	tenant             uuid.UUID
	alice, bob, vendor *identity.Member
}

func newWorld() *world {
	w := &world{store: &fakeStore{members: map[uuid.UUID]*identity.Member{}, name: "Acme"}, audit: &spy{}, tenant: uuid.New()}
	add := func(email string, age time.Duration) *identity.Member {
		m := &identity.Member{ID: uuid.New(), Email: email, Status: identity.StatusActive, CreatedAt: time.Now().Add(-age)}
		w.store.members[m.ID] = m
		return m
	}
	w.alice, w.bob, w.vendor = add("alice@acme.io", 2*time.Hour), add("bob@acme.io", time.Hour), add("agent@vendor.io", time.Hour)
	w.store.admins = []uuid.UUID{w.alice.ID, w.bob.ID}
	deps := kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now,
		IsSupport: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }}
	w.svc = New(w.store, nil, deps)
	return w
}

func (w *world) as(tenant, self uuid.UUID, role string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenant.String(), UserID: self.String(), Roles: []string{role}, HasRoles: true})
}

func statusOf(err error) int {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return 0
}

func TestEnsureNamesTheOldestAdminOnce(t *testing.T) {
	w := newWorld()
	got, err := Ensure(context.Background(), w.store, w.tenant)
	require.NoError(t, err)
	assert.Equal(t, w.alice.ID, got)
	assert.Equal(t, w.alice.ID, w.store.owner, "it is stored, so the answer does not move")
	w.store.admins = []uuid.UUID{w.bob.ID}
	got, _ = Ensure(context.Background(), w.store, w.tenant)
	assert.Equal(t, w.alice.ID, got)

	empty := newWorld()
	empty.store.admins = nil
	got, err = Ensure(context.Background(), empty.store, empty.tenant)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got)
}

func TestGuardProtectsTheOwnerFromOtherAdmins(t *testing.T) {
	w := newWorld()
	c := kernel.Caller{Tenant: w.tenant, Self: w.bob.ID}
	err := Guard(context.Background(), w.store, c, w.alice.ID, "demote")
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.NoError(t, Guard(context.Background(), w.store, c, w.bob.ID, "demote"), "another admin is not protected")
	assert.NoError(t, Guard(context.Background(), w.store, kernel.Caller{Tenant: w.tenant, Self: w.alice.ID}, w.alice.ID, "demote"), "the owner may")
}

func TestOnlyTheOwnerTransfersToAnAdmin(t *testing.T) {
	w := newWorld()
	_, err := w.svc.Transfer(w.as(w.tenant, w.bob.ID, "admin"), &TransferOwnershipRequest{MemberId: w.bob.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	outsider := &identity.Member{ID: uuid.New(), Status: identity.StatusActive}
	w.store.members[outsider.ID] = outsider
	_, err = w.svc.Transfer(w.as(w.tenant, w.alice.ID, "admin"), &TransferOwnershipRequest{MemberId: outsider.ID.String()})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the new owner must be an admin")

	st, err := w.svc.Transfer(w.as(w.tenant, w.alice.ID, "admin"), &TransferOwnershipRequest{MemberId: w.bob.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, w.bob.ID.String(), st.OwnerMemberId)
	require.Len(t, w.audit.entries, 1)
	assert.Equal(t, AuditEntity, w.audit.entries[0].Entity)
}

func TestRecoveryLifecycle(t *testing.T) {
	w := newWorld()
	asVendor := w.as(tenancy.PlatformTenantID, w.vendor.ID, "platform_admin")
	w.store.members[w.vendor.ID] = w.vendor
	// The vendor is looked up in the platform tenant by the fake, which ignores the tenant.
	req := &RequestOwnerRecoveryRequest{TenantId: w.tenant.String(), NewOwnerMemberId: w.bob.ID.String(), Reason: "owner left"}

	_, err := w.svc.RequestRecovery(w.as(w.tenant, w.alice.ID, "admin"), req)
	assert.Equal(t, http.StatusForbidden, statusOf(err), "platform admins only")

	r, err := w.svc.RequestRecovery(asVendor, req)
	require.NoError(t, err)
	assert.NotEmpty(t, r.EffectiveAt)
	_, err = w.svc.RequestRecovery(asVendor, req)
	assert.Equal(t, http.StatusConflict, statusOf(err), "one pending recovery per tenant")

	_, err = w.svc.CompleteRecovery(asVendor, &TenantIdOnlyRequest{TenantId: w.tenant.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err), "not before the waiting period")

	st, err := w.svc.CancelRecovery(w.as(w.tenant, w.alice.ID, "admin"), &kernel.Empty{})
	require.NoError(t, err)
	assert.Nil(t, st.PendingRecovery)
}

func TestCompleteMakesTheNewOwnerAnAdmin(t *testing.T) {
	w := newWorld()
	w.store.members[w.vendor.ID] = w.vendor
	w.store.recovery = &identity.OwnerRecovery{ID: uuid.New(), NewOwnerMember: w.bob.ID, EffectiveAt: time.Now().Add(-time.Minute),
		RequestedByEmail: "agent@vendor.io"}
	w.store.owner = w.alice.ID
	done, err := w.svc.CompleteRecovery(w.as(tenancy.PlatformTenantID, w.vendor.ID, "platform_admin"), &TenantIdOnlyRequest{TenantId: w.tenant.String()})
	require.NoError(t, err)
	assert.NotEmpty(t, done.CompletedAt)
	assert.Equal(t, w.bob.ID, w.store.owner)
	assert.Equal(t, []uuid.UUID{w.bob.ID}, w.store.granted, "ownership without the role would lock the owner out")
}

func TestWorkspaceDeletionNeedsTheNamePasswordAndMFA(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.tenant, w.alice.ID, "admin")
	_, err := w.svc.RequestWorkspaceDeletion(w.as(w.tenant, w.bob.ID, "admin"), &RequestWorkspaceDeletionRequest{ConfirmName: "Acme", Password: "x"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "only the owner")
	_, err = w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "acme", Password: "x"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the name must match exactly")
	_, err = w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a password is required")
	_, err = w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme", Password: "wrong"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	assert.False(t, w.store.deleted)

	w.store.pwOK, w.store.mfa = true, true
	_, err = w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme", Password: "ok"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a second factor is required when enrolled")
	assert.False(t, w.store.deleted)

	w.store.mfaOK = true
	out, err := w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme", Password: "ok", MfaCode: "123456"})
	require.NoError(t, err)
	assert.True(t, w.store.deleted)
	assert.Contains(t, out.CancelLink, "tok", "no mailer: the owner gets the cancel link")
	assert.NotEmpty(t, out.EffectiveAt)

	_, err = w.svc.RequestWorkspaceDeletion(ctx, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme", Password: "ok", MfaCode: "123456"})
	assert.Equal(t, http.StatusConflict, statusOf(err), "already scheduled")
	_ = mail.OwnerRecoveryNotice{}
}
