package policies

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
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type fakeStore struct{ rows []permission.PolicyRow }

func (f *fakeStore) List(_ context.Context, tenant uuid.UUID, entity string, has bool) ([]permission.PolicyRow, error) {
	var out []permission.PolicyRow
	for _, r := range f.rows {
		if r.TenantID == tenant && (!has || r.Entity == entity) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) Create(_ context.Context, tenant uuid.UUID, in NewPolicy) (permission.PolicyRow, error) {
	r := permission.PolicyRow{ID: uuid.New(), TenantID: tenant, Entity: in.Entity, Action: in.Action, Roles: in.Roles,
		HasRoles: in.HasRoles, Subject: in.Subject.String(), Effect: in.Effect, Field: in.Field}
	f.rows = append(f.rows, r)
	return r, nil
}
func (f *fakeStore) Sync(_ context.Context, tenant uuid.UUID, entity string, grants []permission.RoleActionGrant, _ *uuid.UUID) ([]permission.PolicyRow, error) {
	var keep []permission.PolicyRow
	for _, r := range f.rows {
		if !(r.TenantID == tenant && r.Entity == entity) {
			keep = append(keep, r)
		}
	}
	f.rows = keep
	var out []permission.PolicyRow
	for _, g := range grants {
		r := permission.PolicyRow{ID: uuid.New(), TenantID: tenant, Entity: entity, Action: g.Action, HasRoles: g.HasRole,
			Subject: "context", Effect: permission.EffectAllow}
		if g.HasRole {
			r.Roles = []string{g.Role}
		}
		f.rows = append(f.rows, r)
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) error {
	for i, r := range f.rows {
		if r.ID == id {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return nil
}
func (f *fakeStore) Explain(context.Context, *permission.RequestContext, string, string, string, bool, permission.JsonObject, bool) (permission.PolicyExplanation, error) {
	return permission.PolicyExplanation{}, nil
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

type dropped struct{ got []uuid.UUID }

func (d *dropped) Invalidate(_, user uuid.UUID) { d.got = append(d.got, user) }

type world struct {
	svc    *Service
	store  *fakeStore
	audit  *spy
	cache  *dropped
	tenant uuid.UUID
}

func newWorld() *world {
	w := &world{store: &fakeStore{}, audit: &spy{}, cache: &dropped{}, tenant: uuid.New()}
	w.svc = New(w.store, w.cache, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now})
	return w
}

func (w *world) as(role string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: uuid.NewString(), Roles: []string{role}, HasRoles: true})
}

func statusOf(err error) int {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return 0
}

func TestPoliciesNeedAdmin(t *testing.T) {
	w := newWorld()
	_, err := w.svc.List(w.as("viewer"), &ListPoliciesRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.Create(w.as("viewer"), &CreatePolicyRequest{Entity: "e", Action: "read"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Empty(t, w.audit.entries)
}

func TestEveryChangeIsAudited(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	created, err := w.svc.Create(ctx, &CreatePolicyRequest{Entity: "waf.zones", Action: "read", Roles: []string{"viewer"}})
	require.NoError(t, err)
	_, err = w.svc.Seed(ctx, &SeedDefaultPoliciesRequest{Entity: "waf.rules", Roles: []string{"analyst"}, Actions: []string{"read", "update"}})
	require.NoError(t, err)
	role := "analyst"
	_, err = w.svc.SyncMatrix(ctx, &SyncMatrixRequest{Entity: "waf.rules", Grants: []MatrixGrant{{Role: &role, Action: "delete"}}})
	require.NoError(t, err)
	_, err = w.svc.Delete(ctx, &PolicyIDRequest{ID: created.Data.ID})
	require.NoError(t, err)
	require.Len(t, w.audit.entries, 5, "create, two seeded, sync, delete")
	last := w.audit.entries[4]
	assert.Equal(t, audit.ActionDelete, last.Action)
	assert.Contains(t, last.Diff, "entity", "a deleted policy is shown as it was")
	sync := w.audit.entries[3]
	assert.Contains(t, sync.Diff, "grants")
}

func TestBadRequestsChangeNothing(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	_, err := w.svc.Seed(ctx, &SeedDefaultPoliciesRequest{Entity: "e"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.SyncMatrix(ctx, &SyncMatrixRequest{Entity: "e", Grants: []MatrixGrant{{Action: "fly"}}})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.Delete(ctx, &PolicyIDRequest{ID: "nope"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	assert.Empty(t, w.store.rows)
	assert.Empty(t, w.audit.entries)
}

func TestInvalidateContext(t *testing.T) {
	w := newWorld()
	u := uuid.New()
	_, err := w.svc.InvalidateContext(w.as("admin"), &UserIDRequest{UserID: u.String()})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{u}, w.cache.got)
	_, err = w.svc.InvalidateContext(w.as("admin"), &UserIDRequest{UserID: "x"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}
