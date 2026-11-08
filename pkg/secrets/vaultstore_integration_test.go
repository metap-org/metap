//go:build integration

package secrets

import (
	"context"
	"errors"
	"os"
	"testing"

	vaultapi "github.com/openbao/openbao/api/v2"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
)

// These run against a real dev-mode OpenBao (`docker compose up -d vault`, which is OpenBao
// despite the service name): VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=metap-dev-root-token.
// Like every integration test here they fail, never skip, when the server isn't configured.
// The root token only provisions fixtures (policy, AppRole roles); the AppRole tests exercise
// VaultStore with the role's own scoped token.

const testPolicy = "metap-secrets-integration"

func rootClient(t *testing.T) (*vaultapi.Client, string) {
	t.Helper()
	addr, token := os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN")
	if addr == "" || token == "" {
		t.Fatal("VAULT_ADDR and VAULT_TOKEN must be set (e.g. http://127.0.0.1:8200 / metap-dev-root-token, `docker compose up -d vault`)")
	}
	client, err := buildVaultClient(addr, token)
	require.NoError(t, err)
	_, err = client.Sys().HealthWithContext(context.Background())
	require.NoError(t, err, "OpenBao at %s is unreachable", addr)
	return client, addr
}

// provisionAppRole enables the approle backend (idempotent), writes a policy scoped to the
// paths VaultStore uses, and creates a fresh role — returning its role_id and a new secret_id.
func provisionAppRole(t *testing.T, root *vaultapi.Client, role map[string]any) (roleID, secretID string) {
	t.Helper()
	ctx := context.Background()
	auths, err := root.Sys().ListAuthWithContext(ctx)
	require.NoError(t, err)
	if _, ok := auths[defaultApproleMount+"/"]; !ok {
		require.NoError(t, root.Sys().EnableAuthWithOptionsWithContext(ctx, defaultApproleMount, &vaultapi.EnableAuthOptions{Type: "approle"}))
	}
	require.NoError(t, root.Sys().PutPolicyWithContext(ctx, testPolicy, `
path "secret/data/metap/*"     { capabilities = ["create", "read", "update", "delete"] }
path "secret/metadata/metap/*" { capabilities = ["read", "delete"] }
`))

	name := "metap-test-" + uuid.NewString()
	role["token_policies"] = testPolicy
	_, err = root.Logical().WriteWithContext(ctx, "auth/approle/role/"+name, role)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = root.Logical().Delete("auth/approle/role/" + name) })

	r, err := root.Logical().ReadWithContext(ctx, "auth/approle/role/"+name+"/role-id")
	require.NoError(t, err)
	s, err := root.Logical().WriteWithContext(ctx, "auth/approle/role/"+name+"/secret-id", nil)
	require.NoError(t, err)
	return r.Data["role_id"].(string), s.Data["secret_id"].(string)
}

func TestVaultStoreSecretAndDSNRoundTrip(t *testing.T) {
	_, addr := rootClient(t)
	ctx := context.Background()
	store, err := NewVaultStore(addr, os.Getenv("VAULT_TOKEN"))
	require.NoError(t, err)

	ref := "it-" + uuid.NewString()
	require.NoError(t, store.PutSecret(ctx, ref, "s3cret"))
	got, err := store.GetSecret(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "s3cret", got)

	require.NoError(t, store.PutSecret(ctx, ref, "rotated"))
	got, err = store.GetSecret(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "rotated", got)

	require.NoError(t, store.DeleteSecret(ctx, ref))
	_, err = store.GetSecret(ctx, ref)
	require.True(t, errors.Is(err, vaultapi.ErrSecretNotFound), "want not-found after delete, got %v", err)

	dsnRef := "it-dsn-" + uuid.NewString()
	dsn := "postgres://u:p@db:5432/tenant"
	require.NoError(t, store.PutDSN(ctx, dsnRef, dsn))
	creds, err := store.DbCredentials(ctx, dsnRef)
	require.NoError(t, err)
	require.Equal(t, dsn, creds.DSN)
}

// A single-use secret_id can only ever log in once, so this passes only if renewal goes
// through RenewSelf and never re-logs-in (the failure mode vaultstore.go's doc comment names).
func TestVaultStoreAppRoleRenewsWithoutReusingSingleUseSecretID(t *testing.T) {
	root, addr := rootClient(t)
	ctx := context.Background()
	// token_ttl below vaultRenewBuffer: every call sees the token as near expiry and renews.
	roleID, secretID := provisionAppRole(t, root, map[string]any{
		"token_ttl": "30s", "token_max_ttl": "10m", "secret_id_num_uses": 1,
	})
	store, err := NewVaultStoreWithDefaultAppRole(ctx, addr, roleID, secretID)
	require.NoError(t, err)
	tokenBefore := store.client.Token()

	ref := "it-" + uuid.NewString()
	for i := 0; i < 3; i++ {
		require.NoError(t, store.PutSecret(ctx, ref, "v"))
	}
	got, err := store.GetSecret(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "v", got)
	require.Equal(t, tokenBefore, store.client.Token(), "renewal must extend the same token, not log in again")
	require.NoError(t, store.DeleteSecret(ctx, ref))
}

// When the token can no longer be renewed (here: revoked out from under the store), the next
// call falls back to a fresh AppRole login and carries on.
func TestVaultStoreAppRoleFallsBackToReLogin(t *testing.T) {
	root, addr := rootClient(t)
	ctx := context.Background()
	roleID, secretID := provisionAppRole(t, root, map[string]any{"token_ttl": "30s", "token_max_ttl": "10m"})
	store, err := NewVaultStoreWithDefaultAppRole(ctx, addr, roleID, secretID)
	require.NoError(t, err)
	revoked := store.client.Token()
	require.NoError(t, root.Auth().Token().RevokeTreeWithContext(ctx, revoked))

	ref := "it-" + uuid.NewString()
	require.NoError(t, store.PutSecret(ctx, ref, "after-relogin"))
	require.NotEqual(t, revoked, store.client.Token())
	got, err := store.GetSecret(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "after-relogin", got)
	require.NoError(t, store.DeleteSecret(ctx, ref))
}
