// Second SecretStore impl, after EnvStore. Static KV v2 secrets over Vault's HTTP API via
// github.com/hashicorp/vault/api. Two auth methods: a plain token (NewVaultStore) and AppRole
// (NewVaultStoreWithAppRole) — not Vault's dynamic database-credentials engine, still a real
// gap, left for when a real production deployment target exists to need it.
//
// Auto-renewal: an AppRole-authenticated client's token carries a lease_duration; every call
// through SecretStore checks the remembered expiry first and, whenever less than renewBuffer
// remains, renews *before* making the actual Vault call. Renewal tries RenewSelf first —
// extends the existing token's lease without consuming a new AppRole login at all — and only
// falls back to a fresh AppRole login if that fails (token exceeded its max_ttl, or is
// otherwise no longer renewable). This matters concretely if secret_id is provisioned as
// short-lived/one-time (secret_id_num_uses=1): a renewal path that always re-logged-in with the
// same secret_id would permanently break itself the first time it renewed, since Vault rejects
// a reused single-use secret_id. RenewSelf needs no secret_id at all, so it doesn't have this
// problem.
//
// Divergence from the Rust original: that version rebuilds a fresh VaultClient per call outside
// the lock (ring's own vaultrs client doesn't implement Clone). Go's vault/api Client.SetToken
// is just a field mutation on a client that's otherwise safe for concurrent use for read/write
// calls, so this port mutates the token under the lock and reads the client without rebuilding
// it — simpler, same net effect (concurrent calls for different tenants don't serialize behind
// one lock held for a network round trip, since the lock is only held for the token-freshness
// check, not the KV call itself).
package control

import (
	"context"
	"fmt"
	"sync"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

const (
	vaultMount            = "secret"
	vaultDSNPathPrefix    = "metap/dsn"
	vaultSecretPathPrefix = "metap/secret"
	defaultApproleMount   = "approle"
	// vaultRenewBuffer: re-login this long before the current token's remembered expiry, not
	// exactly at it — leaves margin for the login round trip itself plus any clock/measurement
	// slop.
	vaultRenewBuffer = 60 * time.Second
)

type vaultAppRoleConfig struct {
	mount, roleID, secretID string
}

type VaultStore struct {
	client *vaultapi.Client

	mu        sync.Mutex
	expiresAt *time.Time // nil = no known expiry (plain token, or a non-expiring AppRole token)
	// approle is nil for NewVaultStore (plain token) — nothing to re-login with, so
	// ensureFreshToken is always a no-op for that construction path.
	approle *vaultAppRoleConfig
}

func buildVaultClient(addr, token string) (*vaultapi.Client, error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build Vault client: %w", err)
	}
	if token != "" {
		client.SetToken(token)
	}
	return client, nil
}

func expiryFromLease(leaseDurationSeconds int) *time.Time {
	if leaseDurationSeconds == 0 {
		return nil
	}
	t := time.Now().Add(time.Duration(leaseDurationSeconds) * time.Second)
	return &t
}

func NewVaultStore(addr, token string) (*VaultStore, error) {
	client, err := buildVaultClient(addr, token)
	if err != nil {
		return nil, err
	}
	return &VaultStore{client: client}, nil
}

// NewVaultStoreWithAppRole logs in via AppRole (role_id/secret_id) — mount is the AppRole auth
// backend's mount path, "approle" unless an operator has mounted it somewhere else.
func NewVaultStoreWithAppRole(ctx context.Context, addr, mount, roleID, secretID string) (*VaultStore, error) {
	client, err := buildVaultClient(addr, "")
	if err != nil {
		return nil, err
	}
	expiresAt, err := loginApprole(ctx, client, mount, roleID, secretID)
	if err != nil {
		return nil, err
	}
	return &VaultStore{
		client:    client,
		expiresAt: expiresAt,
		approle:   &vaultAppRoleConfig{mount: mount, roleID: roleID, secretID: secretID},
	}, nil
}

func NewVaultStoreWithDefaultAppRole(ctx context.Context, addr, roleID, secretID string) (*VaultStore, error) {
	return NewVaultStoreWithAppRole(ctx, addr, defaultApproleMount, roleID, secretID)
}

func loginApprole(ctx context.Context, client *vaultapi.Client, mount, roleID, secretID string) (*time.Time, error) {
	secret, err := client.Logical().WriteWithContext(ctx, "auth/"+mount+"/login", map[string]any{
		"role_id":   roleID,
		"secret_id": secretID,
	})
	if err != nil {
		return nil, fmt.Errorf("AppRole login failed against mount %s: %w", mount, err)
	}
	if secret == nil || secret.Auth == nil {
		return nil, fmt.Errorf("AppRole login against mount %s returned no auth info", mount)
	}
	client.SetToken(secret.Auth.ClientToken)
	return expiryFromLease(secret.Auth.LeaseDuration), nil
}

// ensureFreshToken renews if less than vaultRenewBuffer remains on the remembered expiry — a
// no-op for a plain-token instance or a token that's still comfortably fresh.
func (s *VaultStore) ensureFreshToken(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approle == nil {
		return nil
	}
	needsRenew := s.expiresAt != nil && time.Now().Add(vaultRenewBuffer).After(*s.expiresAt)
	if !needsRenew {
		return nil
	}

	secret, err := s.client.Auth().Token().RenewSelfWithContext(ctx, 0)
	if err == nil && secret != nil && secret.Auth != nil {
		s.expiresAt = expiryFromLease(secret.Auth.LeaseDuration)
		return nil
	}

	expiresAt, err := loginApprole(ctx, s.client, s.approle.mount, s.approle.roleID, s.approle.secretID)
	if err != nil {
		return fmt.Errorf("AppRole re-login (renewal fallback) failed: %w", err)
	}
	s.expiresAt = expiresAt
	return nil
}

func (s *VaultStore) kv2(ctx context.Context) (*vaultapi.KVv2, error) {
	if err := s.ensureFreshToken(ctx); err != nil {
		return nil, err
	}
	return s.client.KVv2(vaultMount), nil
}

// PutDSN writes the DSN a dsnSecretRef will resolve to.
func (s *VaultStore) PutDSN(ctx context.Context, dsnSecretRef, dsn string) error {
	kv, err := s.kv2(ctx)
	if err != nil {
		return err
	}
	path := vaultDSNPathPrefix + "/" + dsnSecretRef
	if _, err := kv.Put(ctx, path, map[string]any{"dsn": dsn}); err != nil {
		return fmt.Errorf("vault kv2 write failed for dsn_secret_ref %s at %s/%s: %w", dsnSecretRef, vaultMount, path, err)
	}
	return nil
}

func (s *VaultStore) DbCredentials(ctx context.Context, dsnSecretRef string) (*DbCreds, error) {
	kv, err := s.kv2(ctx)
	if err != nil {
		return nil, err
	}
	path := vaultDSNPathPrefix + "/" + dsnSecretRef
	secret, err := kv.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("vault kv2 read failed for dsn_secret_ref %s at %s/%s: %w", dsnSecretRef, vaultMount, path, err)
	}
	dsn, _ := secret.Data["dsn"].(string)
	return &DbCreds{DSN: dsn}, nil
}

func (s *VaultStore) GetSecret(ctx context.Context, secretRef string) (string, error) {
	kv, err := s.kv2(ctx)
	if err != nil {
		return "", err
	}
	path := vaultSecretPathPrefix + "/" + secretRef
	secret, err := kv.Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("vault kv2 read failed for secret %s at %s/%s: %w", secretRef, vaultMount, path, err)
	}
	value, _ := secret.Data["value"].(string)
	return value, nil
}

func (s *VaultStore) PutSecret(ctx context.Context, secretRef, value string) error {
	kv, err := s.kv2(ctx)
	if err != nil {
		return err
	}
	path := vaultSecretPathPrefix + "/" + secretRef
	// The error is deliberately built from the path only — value must never reach a message
	// that ends up in a log or an HTTP response.
	if _, err := kv.Put(ctx, path, map[string]any{"value": value}); err != nil {
		return fmt.Errorf("vault kv2 write failed for secret %s at %s/%s: %w", secretRef, vaultMount, path, err)
	}
	return nil
}

func (s *VaultStore) DeleteSecret(ctx context.Context, secretRef string) error {
	kv, err := s.kv2(ctx)
	if err != nil {
		return err
	}
	path := vaultSecretPathPrefix + "/" + secretRef
	if err := kv.DeleteMetadata(ctx, path); err != nil {
		return fmt.Errorf("vault kv2 delete failed for secret %s at %s/%s: %w", secretRef, vaultMount, path, err)
	}
	return nil
}
