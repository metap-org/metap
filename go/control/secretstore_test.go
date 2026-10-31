package control

// Ported from the Rust metap-control crate's src/secret_store.rs test module.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEnvStoreReadsTheNamedVar(t *testing.T) {
	t.Setenv("METAP_CONTROL_TEST_DSN", "postgres://example/test")
	creds, err := EnvStore{}.DbCredentials(context.Background(), "METAP_CONTROL_TEST_DSN")
	if err != nil {
		t.Fatal(err)
	}
	if creds.DSN != "postgres://example/test" {
		t.Fatalf("got %q", creds.DSN)
	}
}

// The reference is a pure function of (tenant, key) — which is what makes it impossible for one
// tenant's request to name another tenant's secret.
func TestASecretRefIsDerivedFromTheTenantAndNeverCollides(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if TenantSecretRef(a, "cron.webhookAuthorization") != TenantSecretRef(a, "cron.webhookAuthorization") {
		t.Fatal("must be stable")
	}
	if TenantSecretRef(a, "cron.webhookAuthorization") == TenantSecretRef(b, "cron.webhookAuthorization") {
		t.Fatal("different tenants must not collide")
	}
	reference := TenantSecretRef(a, "cron.webhookAuthorization")
	for _, c := range reference {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			t.Fatalf("reference %q is not portable across all four backends", reference)
		}
	}
}

// Refusing is the behavior under test, not an unimplemented stub.
func TestEnvStoreRefusesToWriteASecretAndSaysWhatToSetInstead(t *testing.T) {
	err := EnvStore{}.PutSecret(context.Background(), "METAP_TENANT_X_Y", "s3cret")
	if err == nil {
		t.Fatal("expected a read-only error")
	}
	message := err.Error()
	if !strings.Contains(message, "METAP_TENANT_X_Y") {
		t.Fatalf("%q", message)
	}
	if strings.Contains(message, "s3cret") {
		t.Fatal("the value must never reach an error string")
	}
}

func TestEnvStoreErrorsClearlyWhenVarIsMissing(t *testing.T) {
	_, err := EnvStore{}.DbCredentials(context.Background(), "METAP_CONTROL_TEST_DSN_MISSING")
	if err == nil || !strings.Contains(err.Error(), "METAP_CONTROL_TEST_DSN_MISSING") {
		t.Fatalf("got %v", err)
	}
}
