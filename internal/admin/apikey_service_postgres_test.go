//go:build integration

package admin

import (
	"context"
	"github.com/metap-org/metap/internal/admin/users"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/apikeys"
	"github.com/metap-org/metap/pkg/auth/identity"
)

func TestServiceAccountAndAPIKeyLifecycle(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	asAdmin := h.as(tenant, admin.ID)

	// A service account: listed apart from people, with its roles, no password.
	sa, err := h.client.Admin.CreateServiceAccount(asAdmin, &users.CreateServiceAccountRequest{Name: "ci-bot", Roles: []string{"admin"}})
	require.NoError(t, err)
	assert.Equal(t, "ci-bot", sa.DisplayName)
	assert.Equal(t, identity.ServiceAccountProvider, sa.AuthProvider)
	assert.False(t, sa.HasPassword)
	assert.Equal(t, []string{"admin"}, sa.Roles)
	sas, err := h.client.Admin.ListServiceAccounts(asAdmin, &Empty{})
	require.NoError(t, err)
	require.Len(t, sas.Users, 1)
	people, err := h.client.Admin.List(asAdmin, &users.ListUsersRequest{})
	require.NoError(t, err)
	for _, u := range people.Users {
		assert.NotEqual(t, sa.Id, u.Id, "service accounts are not listed among users")
	}
	_, err = h.client.Admin.CreateServiceAccount(asAdmin, &users.CreateServiceAccountRequest{Name: ""})
	require.Error(t, err, "a name is required")

	// A key authenticates as the account, with its roles, and the secret is only in the response.
	created, err := h.client.Keys.Create(asAdmin, &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "pipeline", ExpiresInDays: 30})
	require.NoError(t, err)
	require.True(t, identity.IsAPIKey(created.Token))
	assert.NotEmpty(t, created.ApiKey.ExpiresAt)
	assert.Contains(t, created.Token, created.ApiKey.Prefix)
	asKey := CallContext(context.Background(), created.Token)
	me, err := h.client.Account.Get(asKey, &Empty{})
	require.NoError(t, err)
	assert.Equal(t, sa.Id, me.UserId)
	_, err = h.client.Admin.List(asKey, &users.ListUsersRequest{})
	require.NoError(t, err, "the account's admin role applies to its key")

	keys, err := h.client.Keys.List(asAdmin, sa.Id)
	require.NoError(t, err)
	require.Len(t, keys.ApiKeys, 1)
	assert.NotEmpty(t, keys.ApiKeys[0].LastUsedAt, "use is stamped")
	assert.Empty(t, keys.ApiKeys[0].RevokedAt)

	// Unknown, malformed and cross-tenant: refused.
	_, err = h.client.Account.Get(CallContext(context.Background(), "mk_not-a-real-key"), &Empty{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))
	other := uuid.New()
	otherAdmin := h.member(other, "admin@other.io", "admin")
	err = h.client.Keys.Revoke(h.as(other, otherAdmin.ID), created.ApiKey.Id)
	assert.Error(t, err, "another tenant's admin cannot revoke the key")
	_, err = h.client.Keys.Create(h.as(other, otherAdmin.ID), &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "x"})
	assert.Error(t, err, "a key cannot be minted for another tenant's member")

	// A non-admin cannot manage keys.
	_, err = h.client.Keys.Create(h.as(tenant, bob.ID), &apikeys.CreateApiKeyRequest{UserId: bob.ID.String(), Name: "mine"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// Disabling the account stops its keys at once, and enabling it again does not bring them back:
	// a key issued before the disable stays dead (the comparison is to the second, so the key is
	// backdated to keep the test off a second boundary). A key made afterwards works.
	_, err = h.pool.Exec(context.Background(), `UPDATE metadata.api_keys SET created_at = now() - interval '1 hour' WHERE id = $1`, created.ApiKey.Id)
	require.NoError(t, err)
	_, err = h.client.Admin.Disable(asAdmin, &users.UserIdRequest{Id: sa.Id})
	require.NoError(t, err)
	_, err = h.client.Account.Get(asKey, &Empty{})
	assert.Error(t, err)
	_, err = h.client.Admin.Enable(asAdmin, &users.UserIdRequest{Id: sa.Id})
	require.NoError(t, err)
	_, err = h.client.Account.Get(asKey, &Empty{})
	assert.Error(t, err, "a key issued before the disable stays revoked after the account is enabled again")
	fresh, err := h.client.Keys.Create(asAdmin, &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "after"})
	require.NoError(t, err)
	_, err = h.client.Account.Get(CallContext(context.Background(), fresh.Token), &Empty{})
	require.NoError(t, err)

	// Revoking kills it, idempotently.
	err = h.client.Keys.Revoke(asAdmin, created.ApiKey.Id)
	require.NoError(t, err)
	err = h.client.Keys.Revoke(asAdmin, created.ApiKey.Id)
	require.NoError(t, err)
	_, err = h.client.Account.Get(asKey, &Empty{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))

	// An expired key is refused.
	past := time.Now().Add(-time.Hour)
	tok, _, err := identity.CreateAPIKey(context.Background(), h.pool, tenant, uuid.MustParse(sa.Id), "old", &past, nil, past.Add(-time.Hour))
	require.NoError(t, err)
	_, err = h.client.Account.Get(CallContext(context.Background(), tok), &Empty{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))
}

// Roadmap 115 M11: a tenant can cap API-key lifetime; with a cap, a key must expire within it.
func TestAPIKeyLifetimeIsCappedByTheTenant(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	asAdmin := h.as(tenant, admin.ID)
	sa, err := h.client.Admin.CreateServiceAccount(asAdmin, &users.CreateServiceAccountRequest{Name: "ci"})
	require.NoError(t, err)

	// No cap: a key that never expires is fine.
	_, err = h.client.Keys.Create(asAdmin, &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "forever"})
	require.NoError(t, err)

	_, err = h.pool.Exec(context.Background(),
		`INSERT INTO metadata.tenant_configs (tenant_id, key, value) VALUES ($1, 'auth.apiKeyMaxLifetimeDays', '90')`, tenant)
	require.NoError(t, err)
	for _, days := range []int32{0, 91, 3650} {
		_, err = h.client.Keys.Create(asAdmin, &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "too long", ExpiresInDays: days})
		assert.Equal(t, http.StatusBadRequest, statusOf(err), "expiresInDays=%d", days)
	}
	_, err = h.client.Keys.Create(asAdmin, &apikeys.CreateApiKeyRequest{UserId: sa.Id, Name: "ok", ExpiresInDays: 90})
	require.NoError(t, err)

	// Another tenant is unaffected.
	otherTenant := uuid.New()
	otherAdmin := h.member(otherTenant, "admin@beta.io", "admin")
	otherSA, err := h.client.Admin.CreateServiceAccount(h.as(otherTenant, otherAdmin.ID), &users.CreateServiceAccountRequest{Name: "ci"})
	require.NoError(t, err)
	_, err = h.client.Keys.Create(h.as(otherTenant, otherAdmin.ID), &apikeys.CreateApiKeyRequest{UserId: otherSA.Id, Name: "forever"})
	require.NoError(t, err)
}
