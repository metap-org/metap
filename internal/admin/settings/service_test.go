package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

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

// fakeStore is an in-memory Store: a fleet tier and per-tenant overrides over the real registry's keys.
type fakeStore struct {
	platform map[string]json.RawMessage
	tenant   map[[2]string]json.RawMessage // (tenant, key)
	failWith error
}

func newFake() *fakeStore {
	return &fakeStore{platform: map[string]json.RawMessage{}, tenant: map[[2]string]json.RawMessage{}}
}

func (f *fakeStore) PlatformView() []PlatformEntry {
	var out []PlatformEntry
	for i := range config.Registry {
		d := &config.Registry[i]
		if d.Level == config.LevelPlatformGlobal || d.Level == config.LevelTenant {
			out = append(out, PlatformEntry{Def: d, Value: f.platform[d.Key]})
		}
	}
	return out
}
func (f *fakeStore) PlatformValue(key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return f.platform[key]
}
func (f *fakeStore) SetPlatform(_ context.Context, key string, v json.RawMessage) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.platform[key] = v
	return nil
}
func (f *fakeStore) ResetPlatform(_ context.Context, key string) error {
	delete(f.platform, key)
	return nil
}
func (f *fakeStore) TenantView(_ context.Context, tenant uuid.UUID) []TenantEntry {
	var out []TenantEntry
	for i := range config.Registry {
		d := &config.Registry[i]
		if d.Level == config.LevelTenant {
			v, over := f.tenant[[2]string{tenant.String(), d.Key}]
			out = append(out, TenantEntry{Def: d, Value: v, Overridden: over})
		}
	}
	return out
}
func (f *fakeStore) TenantValue(_ context.Context, tenant uuid.UUID, key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return f.tenant[[2]string{tenant.String(), key}]
}
func (f *fakeStore) SetTenant(_ context.Context, tenant uuid.UUID, key string, v json.RawMessage) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.tenant[[2]string{tenant.String(), key}] = v
	return nil
}
func (f *fakeStore) ResetTenant(_ context.Context, tenant uuid.UUID, key string) error {
	delete(f.tenant, [2]string{tenant.String(), key})
	return nil
}
func (f *fakeStore) DefaultFor(key string) json.RawMessage { return json.RawMessage(`"declared"`) }

type fixedScope struct{ tenant uuid.UUID }

func (f fixedScope) ScopedTenant(*permission.RequestContext) (uuid.UUID, error) { return f.tenant, nil }

type world struct {
	svc    *Service
	store  *fakeStore
	audit  *auditSpy
	tenant uuid.UUID
}

func newWorld() *world {
	w := &world{store: newFake(), audit: &auditSpy{}, tenant: uuid.New()}
	w.svc = New(w.store, fixedScope{w.tenant}, kernel.Deps{Cfg: kernel.Config{Audit: w.audit}})
	return w
}

func (w *world) as(tenant uuid.UUID, roles ...string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenant.String(), UserID: uuid.NewString(), Roles: roles, HasRoles: true})
}

func (w *world) root(roles ...string) context.Context {
	return w.as(uuid.Nil, append([]string{"platform_admin"}, roles...)...)
}

func status(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

// A change to a credential key records that it changed, never to what.
func TestAConfigChangeNeverRecordsTheValueOfASecretKey(t *testing.T) {
	require.NotNil(t, config.Lookup(config.CronWebhookAuthorization))
	require.True(t, config.Lookup(config.CronWebhookAuthorization).Secret, "the premise: this key is a credential")
	d := diff(config.CronWebhookAuthorization, json.RawMessage(`"Bearer old-secret"`), json.RawMessage(`"Bearer new-secret"`))
	raw, _ := json.Marshal(d)
	require.NotContains(t, string(raw), "old-secret")
	require.NotContains(t, string(raw), "new-secret")
	require.Contains(t, string(raw), "(secret)")
}

func TestADiffHasBeforeAndAfterAndNullWhereThereWasNothing(t *testing.T) {
	d := diff("auth.passwordMinLength", json.RawMessage(`8`), json.RawMessage(`12`))
	require.Equal(t, [2]any{json.RawMessage(`8`), json.RawMessage(`12`)}, d["auth.passwordMinLength"])
	d = diff("auth.passwordMinLength", nil, json.RawMessage(`12`))
	require.Nil(t, d["auth.passwordMinLength"][0])
}

func TestAKeyThatAppliesOnlyAfterARestartSaysSo(t *testing.T) {
	require.False(t, appliesImmediately(config.HTTPRateLimitBurst))
	require.True(t, appliesImmediately("auth.passwordMinLength"))
}

func TestConfigErrorsAreMappedToTheirStatuses(t *testing.T) {
	for kind, want := range map[config.ErrorKind]int{
		config.ErrUnknownKey: http.StatusNotFound, config.ErrNotWritable: http.StatusForbidden, config.ErrInvalid: http.StatusUnprocessableEntity} {
		require.Equal(t, want, status(mapErr(&config.Error{Kind: kind, Key: "k", Reason: "r"})), int(kind))
	}
	boom := context.Canceled
	require.Equal(t, boom, mapErr(boom), "not a config error: unchanged")
}

func TestOnlyAPlatformAdminReadsAndWritesThePlatformTier(t *testing.T) {
	w := newWorld()
	for _, ctx := range []context.Context{w.as(w.tenant, "admin"), w.as(w.tenant)} {
		_, err := w.svc.ListPlatform(ctx)
		require.Equal(t, http.StatusForbidden, status(err))
		_, err = w.svc.SetPlatform(ctx, &SetConfigRequest{Key: "auth.passwordMinLength", Value: []byte(`12`)})
		require.Equal(t, http.StatusForbidden, status(err))
	}
	list, err := w.svc.ListPlatform(w.root())
	require.NoError(t, err)
	require.NotEmpty(t, list.Data)
	for _, item := range list.Data {
		require.NotEqual(t, "operator", item.Level, "operator keys are not writable by anyone here")
	}
}

func TestAPlatformChangeIsMadeAndAuditedWithBeforeAndAfter(t *testing.T) {
	w := newWorld()
	w.store.platform["auth.passwordMinLength"] = json.RawMessage(`8`)
	res, err := w.svc.SetPlatform(w.root(), &SetConfigRequest{Key: "auth.passwordMinLength", Value: []byte(`12`)})
	require.NoError(t, err)
	require.JSONEq(t, `12`, string(res.Data.Value))
	require.True(t, res.Data.AppliesImmediately)

	require.Len(t, w.audit.entries, 1)
	e := w.audit.entries[0]
	require.Equal(t, AuditEntityPlatform, e.Entity)
	require.Equal(t, kernel.NamedID("config", "auth.passwordMinLength"), e.RecordID)
	require.JSONEq(t, `{"before":8,"after":12}`, string(e.Diff["auth.passwordMinLength"]))
}

func TestARefusedChangeIsNotAudited(t *testing.T) {
	w := newWorld()
	w.store.failWith = &config.Error{Kind: config.ErrInvalid, Key: "auth.passwordMinLength", Reason: "too small"}
	_, err := w.svc.SetPlatform(w.root(), &SetConfigRequest{Key: "auth.passwordMinLength", Value: []byte(`1`)})
	require.Equal(t, http.StatusUnprocessableEntity, status(err))
	require.Empty(t, w.audit.entries)
}

func TestAnUnknownKeyHasNoValueToAuditAndIsReportedByTheStore(t *testing.T) {
	w := newWorld()
	require.Nil(t, w.store.PlatformValue("not.a.real.key"))
	w.store.failWith = &config.Error{Kind: config.ErrUnknownKey, Key: "not.a.real.key"}
	_, err := w.svc.SetTenant(w.as(w.tenant, "admin"), &SetConfigRequest{Key: "not.a.real.key", Value: []byte(`1`)})
	require.Equal(t, http.StatusNotFound, status(err))
}

func TestResetFallsBackToTheDeclaredDefault(t *testing.T) {
	w := newWorld()
	w.store.platform["auth.passwordMinLength"] = json.RawMessage(`12`)
	res, err := w.svc.ResetPlatform(w.root(), &ConfigKeyRequest{Key: "auth.passwordMinLength"})
	require.NoError(t, err)
	require.NotNil(t, res.Data)
	require.NotContains(t, w.store.platform, "auth.passwordMinLength")
	require.Len(t, w.audit.entries, 1)
}

func TestAnyMemberReadsTheTenantsConfigButOnlyAnAdminWritesIt(t *testing.T) {
	w := newWorld()
	member := w.as(w.tenant)
	list, err := w.svc.ListTenant(member)
	require.NoError(t, err)
	require.NotEmpty(t, list.Data)
	_, err = w.svc.ListTenant(context.Background())
	require.Equal(t, http.StatusUnauthorized, status(err))

	_, err = w.svc.SetTenant(member, &SetConfigRequest{Key: "auth.sessionTtlSeconds", Value: []byte(`3600`)})
	require.Equal(t, http.StatusForbidden, status(err))
	_, err = w.svc.ResetTenant(member, &ConfigKeyRequest{Key: "auth.sessionTtlSeconds"})
	require.Equal(t, http.StatusForbidden, status(err))
}

func TestATenantOverrideIsAuditedInTheTenantsOwnTrailAndMarkedOverridden(t *testing.T) {
	w := newWorld()
	admin := w.as(w.tenant, "admin")
	res, err := w.svc.SetTenant(admin, &SetConfigRequest{Key: "auth.sessionTtlSeconds", Value: []byte(`3600`)})
	require.NoError(t, err)
	require.True(t, res.Data.Overridden)
	require.Len(t, w.audit.entries, 1)
	require.Equal(t, AuditEntityTenant, w.audit.entries[0].Entity)
	require.Equal(t, w.tenant, w.audit.entries[0].TenantID)

	list, _ := w.svc.ListTenant(admin)
	overridden := 0
	for _, item := range list.Data {
		if item.Key == "auth.sessionTtlSeconds" {
			require.True(t, item.Overridden)
			overridden++
		}
	}
	require.Equal(t, 1, overridden)

	back, err := w.svc.ResetTenant(admin, &ConfigKeyRequest{Key: "auth.sessionTtlSeconds"})
	require.NoError(t, err)
	require.False(t, back.Data.Overridden)
	require.JSONEq(t, `"declared"`, string(back.Data.Value), "what the key reads back once the override is gone")
}
