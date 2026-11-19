//go:build integration

package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/identity"
)

func TestServiceAccountAndAPIKeyLifecycle(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	asAdmin := h.as(tenant, admin.ID)

	// A service account: listed apart from people, with its roles, no password.
	sa, err := h.client.Admin.CreateServiceAccount(asAdmin, &pb.CreateServiceAccountRequest{Name: "ci-bot", Roles: []string{"admin"}})
	require.NoError(t, err)
	assert.Equal(t, "ci-bot", sa.GetDisplayName())
	assert.Equal(t, identity.ServiceAccountProvider, sa.GetAuthProvider())
	assert.False(t, sa.GetHasPassword())
	assert.Equal(t, []string{"admin"}, sa.GetRoles())
	sas, err := h.client.Admin.ListServiceAccounts(asAdmin, &pb.Empty{})
	require.NoError(t, err)
	require.Len(t, sas.GetUsers(), 1)
	people, err := h.client.Admin.ListUsers(asAdmin, &pb.ListUsersRequest{})
	require.NoError(t, err)
	for _, u := range people.GetUsers() {
		assert.NotEqual(t, sa.GetId(), u.GetId(), "service accounts are not listed among users")
	}
	_, err = h.client.Admin.CreateServiceAccount(asAdmin, &pb.CreateServiceAccountRequest{Name: ""})
	require.Error(t, err, "a name is required")

	// A key authenticates as the account, with its roles, and the secret is only in the response.
	created, err := h.client.Admin.CreateApiKey(asAdmin, &pb.CreateApiKeyRequest{UserId: sa.GetId(), Name: "pipeline", ExpiresInDays: 30})
	require.NoError(t, err)
	require.True(t, identity.IsAPIKey(created.GetToken()))
	assert.NotEmpty(t, created.GetApiKey().GetExpiresAt())
	assert.Contains(t, created.GetToken(), created.GetApiKey().GetPrefix())
	asKey := CallContext(context.Background(), created.GetToken())
	me, err := h.client.Account.GetMe(asKey, &pb.Empty{})
	require.NoError(t, err)
	assert.Equal(t, sa.GetId(), me.GetUserId())
	_, err = h.client.Admin.ListUsers(asKey, &pb.ListUsersRequest{})
	require.NoError(t, err, "the account's admin role applies to its key")

	keys, err := h.client.Admin.ListApiKeys(asAdmin, &pb.UserIdRequest{Id: sa.GetId()})
	require.NoError(t, err)
	require.Len(t, keys.GetApiKeys(), 1)
	assert.NotEmpty(t, keys.GetApiKeys()[0].GetLastUsedAt(), "use is stamped")
	assert.Empty(t, keys.GetApiKeys()[0].GetRevokedAt())

	// Unknown, malformed and cross-tenant: refused.
	_, err = h.client.Account.GetMe(CallContext(context.Background(), "mk_not-a-real-key"), &pb.Empty{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	other := uuid.New()
	otherAdmin := h.member(other, "admin@other.io", "admin")
	_, err = h.client.Admin.RevokeApiKey(h.as(other, otherAdmin.ID), &pb.ApiKeyIdRequest{Id: created.GetApiKey().GetId()})
	assert.Error(t, err, "another tenant's admin cannot revoke the key")
	_, err = h.client.Admin.CreateApiKey(h.as(other, otherAdmin.ID), &pb.CreateApiKeyRequest{UserId: sa.GetId(), Name: "x"})
	assert.Error(t, err, "a key cannot be minted for another tenant's member")

	// A non-admin cannot manage keys.
	_, err = h.client.Admin.CreateApiKey(h.as(tenant, bob.ID), &pb.CreateApiKeyRequest{UserId: bob.ID.String(), Name: "mine"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// Disabling the account stops its keys at once; enabling brings them back.
	_, err = h.client.Admin.DisableUser(asAdmin, &pb.UserIdRequest{Id: sa.GetId()})
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(asKey, &pb.Empty{})
	assert.Error(t, err)
	_, err = h.client.Admin.EnableUser(asAdmin, &pb.UserIdRequest{Id: sa.GetId()})
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(asKey, &pb.Empty{})
	require.NoError(t, err)

	// Revoking kills it, idempotently.
	_, err = h.client.Admin.RevokeApiKey(asAdmin, &pb.ApiKeyIdRequest{Id: created.GetApiKey().GetId()})
	require.NoError(t, err)
	_, err = h.client.Admin.RevokeApiKey(asAdmin, &pb.ApiKeyIdRequest{Id: created.GetApiKey().GetId()})
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(asKey, &pb.Empty{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// An expired key is refused.
	past := time.Now().Add(-time.Hour)
	tok, _, err := identity.CreateAPIKey(context.Background(), h.pool, tenant, uuid.MustParse(sa.GetId()), "old", &past, nil, past.Add(-time.Hour))
	require.NoError(t, err)
	_, err = h.client.Account.GetMe(CallContext(context.Background(), tok), &pb.Empty{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
