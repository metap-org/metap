//go:build integration

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/metap-org/metap/internal/admin/credentials"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/secrets"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/jobs/cron"
)

// fakeCredentials stores credential values in memory, in the "database" or "external" home.
type fakeCredentials struct {
	caps   CredentialCapabilities
	source map[string]string
	value  map[string]string
}

func newFakeCredentials(caps CredentialCapabilities) *fakeCredentials {
	return &fakeCredentials{caps: caps, source: map[string]string{}, value: map[string]string{}}
}

func (f *fakeCredentials) Capabilities() CredentialCapabilities { return f.caps }
func (f *fakeCredentials) Put(_ context.Context, ref, source, value string) error {
	if source == "" {
		return errors.New("no source")
	}
	f.source[ref], f.value[ref] = source, value
	return nil
}
func (f *fakeCredentials) Get(_ context.Context, ref string) (string, error) {
	if v, ok := f.value[ref]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (f *fakeCredentials) Remove(_ context.Context, ref string) error {
	delete(f.source, ref)
	delete(f.value, ref)
	return nil
}

func credentialNamed(v *credentials.CredentialsView, name string) *credentials.Credential {
	for _, c := range v.Credentials {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// The Credentials tab keeps many named credentials: add, list, rename, replace the value, move
// between the two homes (which needs a value), and remove one only when no job uses it.
func TestCredentialsAdmin(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	plain := h.member(tenant, "plain@acme.io")
	asAdmin := h.as(tenant, admin.ID)

	fake := newFakeCredentials(CredentialCapabilities{Database: true, External: true, ExternalKind: "vault"})
	h.cfg.Credentials = fake
	svc := credentials.New(credentials.NewPgStore(kernel.Deps{Cfg: h.cfg}), h.cfg.Credentials, kernel.Deps{Cfg: h.cfg})

	view, err := svc.List(asAdmin, &Empty{})
	require.NoError(t, err)
	assert.Empty(t, view.Credentials)
	assert.True(t, view.DatabaseAvailable)
	assert.True(t, view.ExternalAvailable)
	assert.Equal(t, "vault", view.ExternalKind)

	// Two credentials, in different homes; the value goes to the store, never into the view.
	view, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "CRM webhook", Description: "Auth for the CRM",
		Kind: credentials.KindAuthorization, Source: credentials.SourceDatabase, Value: "Bearer abc"})
	require.NoError(t, err)
	view, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "Billing API key", Kind: credentials.KindSecret,
		Source: credentials.SourceExternal, Value: "key-123"})
	require.NoError(t, err)
	require.Len(t, view.Credentials, 2)
	crm := credentialNamed(view, "CRM webhook")
	require.NotNil(t, crm)
	crmID := uuid.MustParse(crm.Id)
	crmRef := secrets.TenantCredentialRef(tenant, crmID)
	assert.Equal(t, credentials.SourceDatabase, crm.Source)
	assert.Equal(t, "Bearer abc", fake.value[crmRef])
	assert.Equal(t, "database", fake.source[crmRef])
	assert.Equal(t, credentials.SourceExternal, credentialNamed(view, "Billing API key").Source)

	// Checks: unique name (case-insensitive), kind/value rules, role.
	_, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "crm WEBHOOK", Kind: credentials.KindSecret, Source: credentials.SourceDatabase, Value: "x"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "names are unique per workspace")
	_, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "Bad header", Kind: credentials.KindAuthorization, Source: credentials.SourceDatabase, Value: "a\nb"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "an Authorization value is a one-line header value")
	_, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "Any secret", Kind: credentials.KindSecret, Source: credentials.SourceDatabase, Value: "line1\nline2"})
	assert.NoError(t, err, "a generic secret may hold anything")
	_, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "n", Kind: "weird", Source: credentials.SourceDatabase, Value: "x"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = svc.List(h.as(tenant, plain.ID), &Empty{})
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// Rename and replace the value (same home).
	newName, newValue := "CRM webhook (prod)", "Bearer rotated"
	view, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: crm.Id, Name: &newName, Value: &newValue})
	require.NoError(t, err)
	assert.NotNil(t, credentialNamed(view, newName))
	assert.Equal(t, "Bearer rotated", fake.value[crmRef])

	// Moving needs a value; with one it lands in the new home.
	external := credentials.SourceExternal
	_, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: crm.Id, Source: &external})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a stored value is never read back, so a move needs one")
	moved := "Bearer moved"
	view, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: crm.Id, Source: &external, Value: &moved})
	require.NoError(t, err)
	assert.Equal(t, credentials.SourceExternal, credentialNamed(view, newName).Source)
	assert.Equal(t, "external", fake.source[crmRef])

	// Another tenant cannot see or touch it.
	other := uuid.New()
	otherAdmin := h.member(other, "admin@other.io", "admin")
	otherView, err := svc.List(h.as(other, otherAdmin.ID), &Empty{})
	require.NoError(t, err)
	assert.Empty(t, otherView.Credentials)
	_, err = svc.Delete(h.as(other, otherAdmin.ID), &credentials.CredentialIdRequest{Id: crm.Id})
	assert.Equal(t, http.StatusNotFound, statusOf(err))

	// A job that uses it blocks removal, and is listed as a user.
	job, err := cron.CreateJob(context.Background(), h.pool, tenant, cron.NewCronJob{Name: "notify CRM", TriggerType: "schedule",
		CronExpr: strPtr("0 * * * *"), Timezone: "UTC", TargetType: "webhook", DispatchMode: "direct", MaxAttempts: 1, Enabled: true,
		TargetConfig: json.RawMessage(`{"url":"https://example.com/hook","credentialId":"` + crm.Id + `"}`)}, nil)
	require.NoError(t, err)
	view, err = svc.List(asAdmin, &Empty{})
	require.NoError(t, err)
	used := credentialNamed(view, newName).UsedBy
	require.Len(t, used, 1)
	assert.Equal(t, "notify CRM", used[0].Name)
	assert.Equal(t, job.ID.String(), used[0].Id)
	assert.Empty(t, credentialNamed(view, "Billing API key").UsedBy)
	_, err = svc.Delete(asAdmin, &credentials.CredentialIdRequest{Id: crm.Id})
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, statusOf(err))
	assert.Equal(t, []string{"notify CRM"}, asServiceError(err).FieldErrors["usedBy"])

	require.NoError(t, cron.DeleteJob(context.Background(), h.pool, tenant, job.ID))
	view, err = svc.Delete(asAdmin, &credentials.CredentialIdRequest{Id: crm.Id})
	require.NoError(t, err)
	assert.Nil(t, credentialNamed(view, newName))
	assert.NotContains(t, fake.value, crmRef, "removing deletes the stored value too")

	// A home the deployment does not offer is refused.
	fake.caps = CredentialCapabilities{Database: false, External: false, ExternalKind: "env"}
	_, err = svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "No home", Kind: credentials.KindSecret, Source: credentials.SourceDatabase, Value: "x"})
	assert.Equal(t, http.StatusConflict, statusOf(err))

	// The audit trail names credentials and their homes, never a value.
	require.NoError(t, audit.DrainAll(context.Background(), h.pool, audit.NewPostgresStore(h.pool)))
	rows, err := audit.NewPostgresStore(h.pool).Search(context.Background(), tenant, audit.SearchQuery{Entity: credentials.AuditEntity})
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		raw, _ := json.Marshal(row.Diff)
		for _, secret := range []string{"Bearer", "key-123", "line1"} {
			assert.NotContains(t, string(raw), secret)
		}
	}
}

func strPtr(s string) *string { return &s }

// Without a credential store the tab answers 501 rather than pretending.
func TestCredentialsUnavailableWithoutStore(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	h.cfg.Credentials = nil
	_, err := credentials.New(credentials.NewPgStore(kernel.Deps{Cfg: h.cfg}), h.cfg.Credentials, kernel.Deps{Cfg: h.cfg}).List(h.as(tenant, admin.ID), &Empty{})
	assert.Equal(t, http.StatusNotImplemented, statusOf(err))
}

// A username-and-password credential: the username is shown, the password is write-only and kept
// with it as one stored value; a new username keeps the password; webhooks send it as Basic.
func TestCredentialsUserPass(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	asAdmin := h.as(tenant, admin.ID)
	fake := newFakeCredentials(CredentialCapabilities{Database: true, External: true, ExternalKind: "vault"})
	h.cfg.Credentials = fake
	svc := credentials.New(credentials.NewPgStore(kernel.Deps{Cfg: h.cfg}), h.cfg.Credentials, kernel.Deps{Cfg: h.cfg})

	view, err := svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "Partner API", Kind: credentials.KindUserPass,
		Username: "svc-user", Source: credentials.SourceDatabase, Value: "p@ss:word"})
	require.NoError(t, err)
	cred := credentialNamed(view, "Partner API")
	require.NotNil(t, cred)
	assert.Equal(t, credentials.KindUserPass, cred.Kind)
	assert.Equal(t, "svc-user", cred.Username, "the username is shown")
	ref := secrets.TenantCredentialRef(tenant, uuid.MustParse(cred.Id))
	user, pass, ok := secrets.DecodeUserPass(fake.value[ref])
	require.True(t, ok, "stored as one value")
	assert.Equal(t, []string{"svc-user", "p@ss:word"}, []string{user, pass})
	assert.Equal(t, "Basic "+"c3ZjLXVzZXI6cEBzczp3b3Jk", secrets.AuthorizationFromCredential(fake.value[ref]), "a webhook sends Basic")

	// Input rules.
	for name, req := range map[string]*credentials.CreateCredentialRequest{
		"no username":      {Name: "a", Kind: credentials.KindUserPass, Source: credentials.SourceDatabase, Value: "x"},
		"colon in user":    {Name: "b", Kind: credentials.KindUserPass, Username: "a:b", Source: credentials.SourceDatabase, Value: "x"},
		"username on kind": {Name: "c", Kind: credentials.KindSecret, Username: "u", Source: credentials.SourceDatabase, Value: "x"},
		"reserved prefix":  {Name: "d", Kind: credentials.KindSecret, Source: credentials.SourceDatabase, Value: "userpass:{}"},
	} {
		_, err := svc.Create(asAdmin, req)
		assert.Equal(t, http.StatusBadRequest, statusOf(err), name)
	}

	// A new username keeps the password; a new password keeps the username.
	rename := "renamed-user"
	view, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: cred.Id, Username: &rename})
	require.NoError(t, err)
	assert.Equal(t, rename, credentialNamed(view, "Partner API").Username)
	user, pass, _ = secrets.DecodeUserPass(fake.value[ref])
	assert.Equal(t, []string{rename, "p@ss:word"}, []string{user, pass})
	newPass := "n3w-pass"
	_, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: cred.Id, Value: &newPass})
	require.NoError(t, err)
	user, pass, _ = secrets.DecodeUserPass(fake.value[ref])
	assert.Equal(t, []string{rename, "n3w-pass"}, []string{user, pass})

	// A username on another kind is refused.
	secret, err := svc.Create(asAdmin, &credentials.CreateCredentialRequest{Name: "Plain", Kind: credentials.KindSecret, Source: credentials.SourceDatabase, Value: "s"})
	require.NoError(t, err)
	_, err = svc.Update(asAdmin, &credentials.UpdateCredentialRequest{Id: credentialNamed(secret, "Plain").Id, Username: &rename})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))
}
