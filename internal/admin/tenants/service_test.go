package tenants

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
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

type fakeStore struct {
	rows        map[uuid.UUID]*tenancy.TenantDetail
	deprovision []uuid.UUID
	failAdmin   bool
}

func (f *fakeStore) Detail(_ context.Context, id uuid.UUID) (*tenancy.TenantDetail, error) {
	if d, ok := f.rows[id]; ok {
		c := *d
		return &c, nil
	}
	return nil, tenancy.ErrTenantNotFound
}

func (f *fakeStore) Search(_ context.Context, flt tenancy.TenantFilter) ([]tenancy.TenantDetail, int, error) {
	var out []tenancy.TenantDetail
	for _, d := range f.rows {
		if flt.Tier == "" || d.Tier == flt.Tier {
			out = append(out, *d)
		}
	}
	return out, len(out), nil
}

func (f *fakeStore) MemberCount(context.Context, uuid.UUID) (int32, error) { return 1, nil }

func (f *fakeStore) Register(_ context.Context, id uuid.UUID, name, tier string) error {
	d := &tenancy.TenantDetail{Name: name}
	d.ID, d.Tier = id, tier
	f.rows[id] = d
	return nil
}

func (f *fakeStore) Deprovision(_ context.Context, id uuid.UUID) error {
	f.deprovision = append(f.deprovision, id)
	delete(f.rows, id)
	return nil
}

func (f *fakeStore) SetProduct(_ context.Context, id uuid.UUID, p string) error {
	f.rows[id].Product = &p
	return nil
}

func (f *fakeStore) CreateAdmin(_ context.Context, tenant uuid.UUID, in NewAdmin, _ *uuid.UUID, now time.Time) (*Admin, error) {
	if f.failAdmin {
		return nil, errors.New("boom")
	}
	return &Admin{Member: &identity.Member{ID: uuid.New(), Email: in.Email},
		Token: "tok", Invitation: &identity.Invitation{ExpiresAt: now.Add(time.Hour)}, NewAccount: true}, nil
}

func (f *fakeStore) Update(_ context.Context, id uuid.UUID, name, tier *string) error {
	d, ok := f.rows[id]
	if !ok {
		return tenancy.ErrTenantNotFound
	}
	if name != nil {
		d.Name = *name
	}
	if tier != nil {
		d.Tier = *tier
	}
	return nil
}

func (f *fakeStore) Suspend(_ context.Context, id uuid.UUID) error {
	d, ok := f.rows[id]
	if !ok {
		return tenancy.ErrTenantNotFound
	}
	if d.Status == tenancy.StatusSuspended {
		return tenancy.ErrTenantState
	}
	d.Status = tenancy.StatusSuspended
	return nil
}

func (f *fakeStore) Resume(_ context.Context, id uuid.UUID) error {
	d, ok := f.rows[id]
	if !ok {
		return tenancy.ErrTenantNotFound
	}
	d.Status = tenancy.StatusActive
	return nil
}

func (f *fakeStore) RequestDeletion(_ context.Context, id uuid.UUID, now time.Time, r time.Duration) error {
	d, ok := f.rows[id]
	if !ok {
		return tenancy.ErrTenantNotFound
	}
	at, eff := now, now.Add(r)
	d.DeletionRequestedAt, d.DeletionEffectiveAt = &at, &eff
	return nil
}

func (f *fakeStore) CancelDeletion(_ context.Context, id uuid.UUID, _ time.Time) error {
	d, ok := f.rows[id]
	if !ok {
		return tenancy.ErrTenantNotFound
	}
	d.DeletionRequestedAt, d.DeletionEffectiveAt = nil, nil
	return nil
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
	svc   *Service
	store *fakeStore
	audit *auditSpy
}

func newWorld() *world {
	w := &world{store: &fakeStore{rows: map[uuid.UUID]*tenancy.TenantDetail{}}, audit: &auditSpy{}}
	w.svc = New(w.store, nil, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now})
	return w
}

func platform() context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenancy.PlatformTenantID.String(), UserID: uuid.NewString(), Roles: []string{"platform_admin"}, HasRoles: true})
}

func statusOf(err error) int {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return 0
}

func TestOnlyPlatformAdminsAdministerTenants(t *testing.T) {
	w := newWorld()
	tenantAdmin := kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true})
	_, err := w.svc.List(tenantAdmin, &ListTenantsRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.List(context.Background(), &ListTenantsRequest{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))
}

func TestCreateMakesTenantAndAuditsIt(t *testing.T) {
	w := newWorld()
	res, err := w.svc.Create(platform(), &CreateTenantRequest{Name: " Acme ", AdminEmail: "a@acme.io", Product: "waf"})
	require.NoError(t, err)
	assert.Equal(t, "Acme", res.GetTenant().GetName())
	assert.Equal(t, "waf", res.GetTenant().GetProduct())
	assert.False(t, res.GetEmailSent(), "no mailer: the answer carries the link instead")
	require.Len(t, w.audit.entries, 1)
	assert.Equal(t, AuditEntity, w.audit.entries[0].Entity)
	assert.Empty(t, w.store.deprovision)
}

func TestCreateRejectsBadInput(t *testing.T) {
	w := newWorld()
	_, err := w.svc.Create(platform(), &CreateTenantRequest{Name: " ", AdminEmail: "a@b.io"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	assert.Empty(t, w.store.rows, "nothing registered for an invalid request")
}

func TestCreateFailureLeavesNoTenant(t *testing.T) {
	w := newWorld()
	w.store.failAdmin = true
	_, err := w.svc.Create(platform(), &CreateTenantRequest{Name: "Acme", AdminEmail: "a@acme.io"})
	require.Error(t, err)
	assert.Len(t, w.store.deprovision, 1, "the registry row is marked deleted again")
	assert.Empty(t, w.store.rows)
	assert.Empty(t, w.audit.entries, "no audit entry for a change that did not happen")
}

func TestLifecycle(t *testing.T) {
	w := newWorld()
	res, err := w.svc.Create(platform(), &CreateTenantRequest{Name: "Acme", AdminEmail: "a@acme.io"})
	require.NoError(t, err)
	id := res.GetTenant().GetId()

	name := "Acme 2"
	up, err := w.svc.Update(platform(), &UpdateTenantRequest{Id: id, Name: &name})
	require.NoError(t, err)
	assert.Equal(t, "Acme 2", up.GetName())

	_, err = w.svc.Suspend(platform(), &TenantIdRequest{Id: id})
	require.NoError(t, err)
	_, err = w.svc.Suspend(platform(), &TenantIdRequest{Id: id})
	assert.Equal(t, http.StatusConflict, statusOf(err), "already suspended")
	_, err = w.svc.Resume(platform(), &TenantIdRequest{Id: id})
	require.NoError(t, err)

	del, err := w.svc.RequestDeletion(platform(), &RequestTenantDeletionRequest{Id: id, RetentionDays: 7})
	require.NoError(t, err)
	assert.NotEmpty(t, del.GetDeletionEffectiveAt())
	back, err := w.svc.CancelDeletion(platform(), &TenantIdRequest{Id: id})
	require.NoError(t, err)
	assert.Empty(t, back.GetDeletionEffectiveAt())

	_, err = w.svc.Get(platform(), &TenantIdRequest{Id: uuid.NewString()})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = w.svc.Get(platform(), &TenantIdRequest{Id: "nope"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}
