package credentials

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
	"github.com/metap-org/metap/pkg/secrets"
)

type fakeStore struct {
	rows map[uuid.UUID]Row
	used map[uuid.UUID][]*CredentialUse
}

func newStore() *fakeStore {
	return &fakeStore{rows: map[uuid.UUID]Row{}, used: map[uuid.UUID][]*CredentialUse{}}
}

func (f *fakeStore) taken(name string, except uuid.UUID) bool {
	for id, r := range f.rows {
		if id != except && strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}
func (f *fakeStore) Get(_ context.Context, _, id uuid.UUID) (Row, error) {
	if r, ok := f.rows[id]; ok {
		return r, nil
	}
	return Row{}, ErrNotFound
}
func (f *fakeStore) List(context.Context, uuid.UUID) ([]Row, error) {
	var out []Row
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeStore) Insert(_ context.Context, _ uuid.UUID, r Row, _ *uuid.UUID) error {
	if f.taken(r.Name, r.ID) {
		return ErrNameTaken
	}
	f.rows[r.ID] = r
	return nil
}
func (f *fakeStore) Update(_ context.Context, _ uuid.UUID, r Row) error {
	if f.taken(r.Name, r.ID) {
		return ErrNameTaken
	}
	f.rows[r.ID] = r
	return nil
}
func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) error { delete(f.rows, id); return nil }
func (f *fakeStore) Usage(context.Context, uuid.UUID) (map[uuid.UUID][]*CredentialUse, error) {
	return f.used, nil
}

// vault keeps values by reference, in the home they were put in.
type vault struct {
	caps   kernel.CredentialCapabilities
	values map[string]string
	homes  map[string]string
}

func (v *vault) Capabilities() kernel.CredentialCapabilities { return v.caps }
func (v *vault) Put(_ context.Context, ref, source, value string) error {
	v.values[ref], v.homes[ref] = value, source
	return nil
}
func (v *vault) Get(_ context.Context, ref string) (string, error) {
	if s, ok := v.values[ref]; ok {
		return s, nil
	}
	return "", errors.New("missing")
}
func (v *vault) Remove(_ context.Context, ref string) error {
	delete(v.values, ref)
	delete(v.homes, ref)
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
	svc    *Service
	store  *fakeStore
	vault  *vault
	audit  *spy
	tenant uuid.UUID
}

func newWorld() *world {
	w := &world{store: newStore(), audit: &spy{}, tenant: uuid.New(),
		vault: &vault{caps: kernel.CredentialCapabilities{Database: true, External: true, ExternalKind: "vault"},
			values: map[string]string{}, homes: map[string]string{}}}
	w.svc = New(w.store, w.vault, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now})
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

func (w *world) id(v *CredentialsView, name string) uuid.UUID {
	for _, c := range v.Credentials {
		if c.Name == name {
			return uuid.MustParse(c.Id)
		}
	}
	return uuid.Nil
}

func TestNeedsAdminAndAManager(t *testing.T) {
	w := newWorld()
	_, err := w.svc.List(w.as("viewer"), &kernel.Empty{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	none := New(w.store, nil, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}})
	_, err = none.List(w.as("admin"), &kernel.Empty{})
	assert.Equal(t, http.StatusNotImplemented, statusOf(err))
}

func TestCreateStoresValueAwayFromTheRowAndAuditsWithoutIt(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	v, err := w.svc.Create(ctx, &CreateCredentialRequest{Name: "CRM", Kind: KindAuthorization, Source: SourceDatabase, Value: "Bearer s3cret"})
	require.NoError(t, err)
	id := w.id(v, "CRM")
	ref := secrets.TenantCredentialRef(w.tenant, id)
	assert.Equal(t, "Bearer s3cret", w.vault.values[ref])
	assert.Equal(t, SourceDatabase, w.vault.homes[ref])
	require.Len(t, w.audit.entries, 1)
	for _, d := range w.audit.entries[0].Diff {
		assert.NotContains(t, string(d), "s3cret", "the trail never holds a value")
	}

	_, err = w.svc.Create(ctx, &CreateCredentialRequest{Name: "crm", Kind: KindSecret, Source: SourceDatabase, Value: "x"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a name is unique ignoring case")
	assert.Len(t, w.vault.values, 1, "the value put for the refused row is removed again")
}

func TestUserPassKeepsThePasswordWhenOnlyTheUsernameChanges(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	v, err := w.svc.Create(ctx, &CreateCredentialRequest{Name: "Basic", Kind: KindUserPass, Source: SourceDatabase, Username: "bob", Value: "pw"})
	require.NoError(t, err)
	id := w.id(v, "Basic")
	newName := "alice"
	_, err = w.svc.Update(ctx, &UpdateCredentialRequest{Id: id.String(), Username: &newName})
	require.NoError(t, err)
	user, pass, ok := secrets.DecodeUserPass(w.vault.values[secrets.TenantCredentialRef(w.tenant, id)])
	require.True(t, ok)
	assert.Equal(t, "alice", user)
	assert.Equal(t, "pw", pass)
}

func TestMovingNeedsAValueAndAHomeThatExists(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	v, err := w.svc.Create(ctx, &CreateCredentialRequest{Name: "K", Kind: KindSecret, Source: SourceDatabase, Value: "x"})
	require.NoError(t, err)
	id := w.id(v, "K").String()
	ext := SourceExternal
	_, err = w.svc.Update(ctx, &UpdateCredentialRequest{Id: id, Source: &ext})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "it is never read back, so a move needs the value")
	val := "y"
	_, err = w.svc.Update(ctx, &UpdateCredentialRequest{Id: id, Source: &ext, Value: &val})
	require.NoError(t, err)
	assert.Equal(t, SourceExternal, w.vault.homes[secrets.TenantCredentialRef(w.tenant, uuid.MustParse(id))])
}

func TestDeleteRefusedWhileUsed(t *testing.T) {
	w := newWorld()
	ctx := w.as("admin")
	v, err := w.svc.Create(ctx, &CreateCredentialRequest{Name: "K", Kind: KindSecret, Source: SourceDatabase, Value: "x"})
	require.NoError(t, err)
	id := w.id(v, "K")
	w.store.used[id] = []*CredentialUse{{Kind: "cronJob", Name: "nightly"}}
	_, err = w.svc.Delete(ctx, &CredentialIdRequest{Id: id.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err))
	assert.Len(t, w.vault.values, 1, "nothing removed while it is in use")

	delete(w.store.used, id)
	left, err := w.svc.Delete(ctx, &CredentialIdRequest{Id: id.String()})
	require.NoError(t, err)
	assert.Empty(t, left.Credentials)
	assert.Empty(t, w.vault.values)
	_, err = w.svc.Delete(ctx, &CredentialIdRequest{Id: id.String()})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
}
