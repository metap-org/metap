// SecretStore is an interface, not a concrete type, for the same reason EventBus and PolicyStore
// are — swapping how a DedicatedDb tenant's connection string is resolved without touching
// Router.
package control

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DbCreds carries a resolved tenant DSN. Divergence from the Rust original (secrecy::SecretString,
// whose Debug impl redacts): DSN is a plain Go string here, same documented tradeoff
// storage.S3ObjectStoreConfig already makes in this port — avoid logging a DbCreds value
// directly with %v/%+v.
type DbCreds struct {
	DSN string
	// ExpiresAt is nil for a static credential, never rotated (every case today — EnvStore).
	// Non-nil is for a future dynamic-credential SecretStore (Vault-issued, self-expiring) to
	// signal Router it should refresh the cached pool before this instant — not implemented yet,
	// nothing produces a non-nil value today.
	ExpiresAt *time.Time
}

// TenantSecretRef is the reference under which one tenant's secret for one config key is
// stored — derived, never supplied. There is no request field anywhere that carries a
// reference, so there is nothing to prefix, validate, or get wrong. Same discipline as
// S3ObjectStore building {tenant_id}/{key} internally.
//
// The format is the intersection of what all four backends accept: EnvStore needs a POSIX-ish
// name ([A-Za-z0-9_]), GCP Secret Manager allows only [A-Za-z0-9_-], AWS/Vault are more
// permissive than both. One format for four backends means a secret provisioned for a tenant
// resolves identically whichever backend a deployment runs.
func TenantSecretRef(tenantID uuid.UUID, key string) string {
	var b strings.Builder
	for _, c := range key {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return fmt.Sprintf("METAP_TENANT_%s_%s",
		strings.ToUpper(strings.ReplaceAll(tenantID.String(), "-", "")),
		strings.ToUpper(b.String()))
}

type SecretStore interface {
	// DbCredentials: dsnSecretRef is control.tenants.dsn_secret_ref — Router already resolved
	// the tenant's routing row before calling this, so it's passed the reference directly
	// rather than a tenant id the store would have to look up again itself.
	DbCredentials(ctx context.Context, dsnSecretRef string) (*DbCreds, error)

	// GetSecret reads a general secret — today, the Authorization header value a tenant's cron
	// webhook sends. secretRef must come from TenantSecretRef, never from a request. Returns an
	// error rather than a zero value when nothing is stored.
	GetSecret(ctx context.Context, secretRef string) (string, error)

	// PutSecret stores a general secret, replacing any previous value. A backend that cannot
	// accept writes at runtime (see EnvStore) must return an error here rather than pretend.
	PutSecret(ctx context.Context, secretRef, value string) error

	// DeleteSecret removes a secret. Called when a tenant clears the config key that referenced
	// it, so that clearing the reference genuinely revokes the credential rather than merely
	// hiding it.
	DeleteSecret(ctx context.Context, secretRef string) error
}

// EnvStore reads the DSN straight from an environment variable named dsnSecretRef, verbatim —
// no naming transformation. The default SecretStore, and the one a deployment gets when none of
// Vault/AWS/GCP is configured.
//
// Read-only for general secrets: GetSecret works (an operator can provision a tenant's webhook
// credential by exporting the environment variable TenantSecretRef names), but PutSecret/
// DeleteSecret refuse — writing here would mean os.Setenv on the running process: invisible to
// every other process, lost on restart, and impossible to audit.
type EnvStore struct{}

func (EnvStore) DbCredentials(_ context.Context, dsnSecretRef string) (*DbCreds, error) {
	dsn, ok := os.LookupEnv(dsnSecretRef)
	if !ok {
		return nil, fmt.Errorf("env var %s is not set (dsn_secret_ref lookup)", dsnSecretRef)
	}
	return &DbCreds{DSN: dsn}, nil
}

func (EnvStore) GetSecret(_ context.Context, secretRef string) (string, error) {
	value, ok := os.LookupEnv(secretRef)
	if !ok {
		return "", fmt.Errorf("env var %s is not set (secret lookup)", secretRef)
	}
	return value, nil
}

func (EnvStore) PutSecret(_ context.Context, secretRef, _ string) error {
	return fmt.Errorf("the environment-variable secret backend is read-only: set %s in this deployment's "+
		"environment, or configure Vault/AWS Secrets Manager/GCP Secret Manager to let tenants manage their own secrets", secretRef)
}

func (EnvStore) DeleteSecret(_ context.Context, secretRef string) error {
	return fmt.Errorf("the environment-variable secret backend is read-only: unset %s in this deployment's environment", secretRef)
}
