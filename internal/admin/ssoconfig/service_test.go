package ssoconfig

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
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type fakeStore struct {
	oidc, saml     *sso.OidcConfig
	samlCfg        *sso.SamlConfig
	oidcOn, samlOn bool
	domains        []sso.SSODomain
	checked        bool
	checkResult    bool
	deleted        []string
}

func (f *fakeStore) Oidc(context.Context, uuid.UUID) (*sso.OidcConfig, bool, error) {
	return f.oidc, f.oidcOn, nil
}
func (f *fakeStore) Saml(context.Context, uuid.UUID) (*sso.SamlConfig, bool, error) {
	return f.samlCfg, f.samlOn, nil
}
func (f *fakeStore) ActiveOidc(context.Context, uuid.UUID) (*sso.OidcConfig, error) {
	if f.oidcOn {
		return f.oidc, nil
	}
	return nil, nil
}
func (f *fakeStore) ActiveSaml(context.Context, uuid.UUID) (*sso.SamlConfig, error) {
	if f.samlOn {
		return f.samlCfg, nil
	}
	return nil, nil
}
func (f *fakeStore) SaveOidc(_ context.Context, _ uuid.UUID, c *sso.OidcConfig, on bool, domains []string) error {
	f.oidc, f.oidcOn = c, on
	f.domains = nil
	for _, d := range domains {
		f.domains = append(f.domains, sso.SSODomain{Domain: d, Token: "tok"})
	}
	return nil
}
func (f *fakeStore) SaveSaml(_ context.Context, _ uuid.UUID, c *sso.SamlConfig, on bool, _ []string) error {
	f.samlCfg, f.samlOn = c, on
	return nil
}
func (f *fakeStore) DeleteOidc(context.Context, uuid.UUID) error {
	f.oidc = nil
	f.deleted = append(f.deleted, "oidc")
	return nil
}
func (f *fakeStore) DeleteSaml(context.Context, uuid.UUID) error {
	f.samlCfg = nil
	f.deleted = append(f.deleted, "saml")
	return nil
}
func (f *fakeStore) Domains(context.Context, uuid.UUID) ([]sso.SSODomain, error) {
	return f.domains, nil
}
func (f *fakeStore) CheckDomain(context.Context, uuid.UUID, sso.SSODomain, time.Time) (bool, error) {
	f.checked = true
	return f.checkResult, nil
}

type vault map[string]string

func (v vault) GetSecret(_ context.Context, ref string) (string, error) {
	if s, ok := v[ref]; ok {
		return s, nil
	}
	return "", errors.New("missing")
}
func (v vault) PutSecret(_ context.Context, ref, value string) error { v[ref] = value; return nil }
func (v vault) DeleteSecret(_ context.Context, ref string) error     { delete(v, ref); return nil }

type provider struct {
	reachable bool
	fetched   string
}

func (p *provider) CheckOidc(context.Context, *sso.OidcConfig, string) error {
	if !p.reachable {
		return errors.New("unreachable")
	}
	return nil
}
func (p *provider) FetchSamlMetadata(_ context.Context, url string) ([]byte, error) {
	p.fetched = url
	return nil, errors.New("no network in unit tests")
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
	svc      *Service
	store    *fakeStore
	vault    vault
	provider *provider
	audit    *spy
	tenant   uuid.UUID
	owner    uuid.UUID
}

func newWorld(publicURL string) *world {
	w := &world{store: &fakeStore{}, vault: vault{}, provider: &provider{reachable: true}, audit: &spy{},
		tenant: uuid.New(), owner: uuid.New()}
	deps := kernel.Deps{Cfg: kernel.Config{Audit: w.audit}, NowFor: time.Now,
		IsSupport: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }}
	w.svc = New(w.store, w.vault, w.provider, func(context.Context, uuid.UUID) (uuid.UUID, error) { return w.owner, nil }, deps, publicURL)
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

func valid() *SetSsoConfigRequest {
	return &SetSsoConfigRequest{IssuerUrl: "https://idp.acme.io", ClientId: "c1", ClientSecret: "s3cret", Enabled: true,
		EmailDomains: []string{"acme.io"}}
}

func TestSetOidcStoresTheSecretApartAndAudits(t *testing.T) {
	w := newWorld("https://app.acme.io")
	got, err := w.svc.SetOidc(w.as(w.owner, "admin"), valid())
	require.NoError(t, err)
	assert.True(t, got.Configured)
	assert.True(t, got.HasClientSecret)
	assert.Equal(t, "https://app.acme.io/auth/sso/callback", got.RedirectUri)
	assert.Len(t, w.vault, 1)
	require.Len(t, w.audit.entries, 1)
	for _, d := range w.audit.entries[0].Diff {
		assert.NotContains(t, string(d), "s3cret")
	}
	assert.Len(t, got.Domains, 1)
}

func TestSetOidcRefusals(t *testing.T) {
	w := newWorld("https://app.acme.io")
	ctx := w.as(w.owner, "admin")

	r := valid()
	r.IssuerUrl = "http://idp.acme.io"
	_, err := w.svc.SetOidc(ctx, r)
	assert.Equal(t, http.StatusBadRequest, statusOf(err))

	r = valid()
	r.ClientSecret = ""
	_, err = w.svc.SetOidc(ctx, r)
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a secret is required the first time")

	w.provider.reachable = false
	_, err = w.svc.SetOidc(ctx, valid())
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a wrong issuer is caught now")
	assert.Empty(t, w.vault, "nothing stored for a configuration that was refused")
	assert.Empty(t, w.audit.entries)

	_, err = newWorld("").svc.SetOidc(newWorld("").as(uuid.New(), "admin"), valid())
	assert.Equal(t, http.StatusServiceUnavailable, statusOf(err), "no public URL")

	_, err = w.svc.SetOidc(w.as(w.owner, "viewer"), valid())
	assert.Equal(t, http.StatusForbidden, statusOf(err))
}

func TestOnlyTheOwnerEnforces(t *testing.T) {
	w := newWorld("https://app.acme.io")
	r := valid()
	r.Enforced = true
	_, err := w.svc.SetOidc(w.as(uuid.New(), "admin"), r)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.SetOidc(w.as(w.owner, "admin"), r)
	require.NoError(t, err)
}

func TestOneProtocolAtATime(t *testing.T) {
	w := newWorld("https://app.acme.io")
	ctx := w.as(w.owner, "admin")
	_, err := w.svc.SetOidc(ctx, valid())
	require.NoError(t, err)
	_, err = w.svc.SetSaml(ctx, &SetSamlConfigRequest{Enabled: true, NameIdFormat: sso.NameIDEmail, IdpMetadataXml: "<x/>"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}

func TestSamlNeedsMetadata(t *testing.T) {
	w := newWorld("https://app.acme.io")
	ctx := w.as(w.owner, "admin")
	_, err := w.svc.SetSaml(ctx, &SetSamlConfigRequest{NameIdFormat: sso.NameIDEmail})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "metadata as a URL or XML")
	_, err = w.svc.SetSaml(ctx, &SetSamlConfigRequest{NameIdFormat: sso.NameIDEmail, IdpMetadataUrl: "https://idp/m.xml"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the fetch failed")
	assert.Equal(t, "https://idp/m.xml", w.provider.fetched)
	_, err = w.svc.SetSaml(ctx, &SetSamlConfigRequest{NameIdFormat: sso.NameIDEmail, IdpMetadataXml: "not xml"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}

func TestDeleteRemovesTheSecretAndIsAudited(t *testing.T) {
	w := newWorld("https://app.acme.io")
	ctx := w.as(w.owner, "admin")
	_, err := w.svc.SetOidc(ctx, valid())
	require.NoError(t, err)
	_, err = w.svc.DeleteOidc(ctx, &kernel.Empty{})
	require.NoError(t, err)
	assert.Empty(t, w.vault)
	assert.Equal(t, []string{"oidc"}, w.store.deleted)
	assert.Len(t, w.audit.entries, 2)
	cfg, err := w.svc.GetOidc(ctx, &kernel.Empty{})
	require.NoError(t, err)
	assert.False(t, cfg.Configured)
}

func TestVerifyDomain(t *testing.T) {
	w := newWorld("https://app.acme.io")
	ctx := w.as(w.owner, "admin")
	_, err := w.svc.SetOidc(ctx, valid())
	require.NoError(t, err)

	_, err = w.svc.VerifyDomain(ctx, &VerifySsoDomainRequest{Domain: "other.io"})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = w.svc.VerifyDomain(ctx, &VerifySsoDomainRequest{Domain: "acme.io"})
	assert.Equal(t, http.StatusConflict, statusOf(err), "the record is not there yet")
	w.store.checkResult = true
	_, err = w.svc.VerifyDomain(ctx, &VerifySsoDomainRequest{Domain: " ACME.io "})
	require.NoError(t, err)
	assert.True(t, w.store.checked)
}
