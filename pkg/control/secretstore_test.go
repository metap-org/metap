package control

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEnvStoreReadsTheNamedVar(t *testing.T) {
	t.Setenv("METAP_CONTROL_TEST_DSN", "postgres://example/test")
	creds, err := EnvStore{}.DbCredentials(context.Background(), "METAP_CONTROL_TEST_DSN")
	require.NoError(t, err)
	require.Equal(t, "postgres://example/test", creds.DSN, "got %q", creds.DSN)
}

// The reference is a pure function of (tenant, key) — which is what makes it impossible for one
// tenant's request to name another tenant's secret.
func TestASecretRefIsDerivedFromTheTenantAndNeverCollides(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	require.Equal(t, TenantSecretRef(a, "cron.webhookAuthorization"), TenantSecretRef(a, "cron.webhookAuthorization"), "must be stable")
	require.NotEqual(t, TenantSecretRef(b, "cron.webhookAuthorization"), TenantSecretRef(a, "cron.webhookAuthorization"), "different tenants must not collide")
	reference := TenantSecretRef(a, "cron.webhookAuthorization")
	for _, c := range reference {
		require.True(t, ((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'), "reference %q is not portable across all four backends", reference)
	}
}

// Refusing is the behavior under test, not an unimplemented stub.
func TestEnvStoreRefusesToWriteASecretAndSaysWhatToSetInstead(t *testing.T) {
	err := EnvStore{}.PutSecret(context.Background(), "METAP_TENANT_X_Y", "s3cret")
	require.Error(t, err, "expected a read-only error")
	message := err.Error()
	require.Contains(t, message, "METAP_TENANT_X_Y", "%q", message)
	require.NotContains(t, message, "s3cret", "the value must never reach an error string")
}

func TestEnvStoreErrorsClearlyWhenVarIsMissing(t *testing.T) {
	_, err := EnvStore{}.DbCredentials(context.Background(), "METAP_CONTROL_TEST_DSN_MISSING")
	require.Error(t, err, "got %v", err)
	require.Contains(t, err.Error(), "METAP_CONTROL_TEST_DSN_MISSING", "got %v", err)
}
