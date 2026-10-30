package peripherals

// E2E tests against a real Postgres. Ported from the Rust metap-peripherals crate's
// tests/peripherals_postgres.rs where that file covers role assignment and metadata drift. The
// index_reconciler.rs e2e tests in that file are deliberately NOT ported: they insert into and
// query a table literally named "records", which migrations/0033_drop_records_table.sql
// drops entirely in the current schema — those tests exercise a code path this port's own
// index_reconciler.go doc comment already documents as dead against the current schema, so
// porting them would just assert "fails with relation records does not exist", which is not a
// useful regression test. The pure unit tests (build_index_name/quote_literal/quote_identifier)
// are ported in index_reconciler_test.go instead.
//
// auth.go/context_attributes.go/preferences.go have no dedicated test of their own in the Rust
// crate's test file (confirmed by reading it) — new e2e coverage added during the port.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRoleAssignmentRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	roles, err := GetRolesForUser(ctx, pool, tenantID, userID)
	if err != nil || len(roles) != 0 {
		t.Fatalf("expected no roles yet, got %v err=%v", roles, err)
	}

	if err := AssignRole(ctx, pool, tenantID, userID, "sales", nil); err != nil {
		t.Fatal(err)
	}
	if err := AssignRole(ctx, pool, tenantID, userID, "support", nil); err != nil {
		t.Fatal(err)
	}
	// Duplicate assign is a no-op, not an error.
	if err := AssignRole(ctx, pool, tenantID, userID, "sales", nil); err != nil {
		t.Fatal(err)
	}

	roles, err = GetRolesForUser(ctx, pool, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(roles, "sales", "support") || len(roles) != 2 {
		t.Fatalf("expected [sales support], got %v", roles)
	}

	users, err := ListUsers(ctx, pool, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].UserID != userID {
		t.Fatalf("expected exactly one user, got %+v", users)
	}

	if err := RevokeRole(ctx, pool, tenantID, userID, "sales"); err != nil {
		t.Fatal(err)
	}
	roles, err = GetRolesForUser(ctx, pool, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0] != "support" {
		t.Fatalf("expected [support], got %v", roles)
	}

	pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

func TestMetadataDriftRecordsFirstBootThenDetectsChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entityName := "test.drift." + uuid.NewString()

	Check(ctx, pool, []metadata.EntitySummary{{Name: entityName, Version: "hash-v1"}})
	var stored string
	if err := pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "hash-v1" {
		t.Fatalf("expected hash-v1, got %q", stored)
	}

	// Second call with a different hash — drift is logged, row is updated either way.
	Check(ctx, pool, []metadata.EntitySummary{{Name: entityName, Version: "hash-v2"}})
	if err := pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "hash-v2" {
		t.Fatalf("expected hash-v2, got %q", stored)
	}

	pool.Exec(ctx, "DELETE FROM metadata_versions WHERE entity_name = $1", entityName)
}

func TestCreateUserAndVerifyCredentialsRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	email := uuid.NewString() + "@example.com"

	created, err := CreateUser(ctx, pool, tenantID, email, "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM users WHERE id = $1", created.ID) })

	if created.Email != email || created.TenantID != tenantID {
		t.Fatalf("unexpected created user: %+v", created)
	}

	ok, err := VerifyCredentials(ctx, pool, email, "correct-password")
	if err != nil || ok == nil || ok.ID != created.ID {
		t.Fatalf("expected the correct password to verify, got %+v err=%v", ok, err)
	}

	wrong, err := VerifyCredentials(ctx, pool, email, "wrong-password")
	if err != nil {
		t.Fatal(err)
	}
	if wrong != nil {
		t.Fatal("expected a wrong password to fail verification")
	}

	missing, err := VerifyCredentials(ctx, pool, "does-not-exist-"+uuid.NewString()+"@example.com", "anything")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatal("expected an unknown email to fail verification, not error")
	}

	found, err := FindUserByID(ctx, pool, tenantID, created.ID)
	if err != nil || found == nil || found.Email != email {
		t.Fatalf("expected to find the user by id, got %+v err=%v", found, err)
	}

	// A user id looked up under the wrong tenant must not resolve.
	wrongTenant, err := FindUserByID(ctx, pool, uuid.New(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wrongTenant != nil {
		t.Fatal("expected a cross-tenant lookup to find nothing")
	}

	listed, err := ListTenantUsers(ctx, pool, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	found2 := false
	for _, u := range listed {
		if u.ID == created.ID {
			found2 = true
		}
	}
	if !found2 {
		t.Fatal("expected the created user to appear in ListTenantUsers")
	}
}

func generateTestRSAKeyPair(t *testing.T) (privatePEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), &key.PublicKey
}

func TestMintJWTAndDecodeAccessTokenRoundTrip(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	tenantID := uuid.New()
	userID := uuid.New()

	token, err := MintJWT(privatePEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := DecodeAccessToken(token, publicKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != userID.String() {
		t.Fatalf("expected sub=%s, got %s", userID, claims.Subject)
	}
	if claims.TenantID != tenantID.String() {
		t.Fatalf("expected tenantId=%s, got %s", tenantID, claims.TenantID)
	}
	if claims.JTI == nil || *claims.JTI == "" {
		t.Fatal("expected a non-empty jti")
	}
	if claims.Scope != nil {
		t.Fatal("expected no scope on a plain session token")
	}
}

func TestMintOAuthAccessTokenCarriesScopeAndClientID(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	tenantID := uuid.New()
	userID := uuid.New()

	token, err := MintOAuthAccessToken(privatePEM, tenantID, userID, 3600, "read:zones", "mcl_abc123")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := DecodeAccessToken(token, publicKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Scope == nil || *claims.Scope != "read:zones" {
		t.Fatalf("expected scope=read:zones, got %v", claims.Scope)
	}
	if claims.ClientID == nil || *claims.ClientID != "mcl_abc123" {
		t.Fatalf("expected clientId=mcl_abc123, got %v", claims.ClientID)
	}
}

func TestDecodeAccessTokenRejectsATokenSignedByADifferentKey(t *testing.T) {
	_, publicKey := generateTestRSAKeyPair(t)

	// A token signed with a *different* key must fail verification outright.
	otherPrivatePEM, _ := generateTestRSAKeyPair(t)
	token, err := MintJWT(otherPrivatePEM, uuid.New(), uuid.New(), 3600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAccessToken(token, publicKey, 0); err == nil {
		t.Fatal("expected a token signed by a different key to fail verification")
	}
}

func TestDecodeAccessTokenRejectsAnExpiredToken(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	// A zero/negative TTL mints an already-expired token.
	token, err := MintJWT(privatePEM, uuid.New(), uuid.New(), 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := DecodeAccessToken(token, publicKey, 0); err == nil {
		t.Fatal("expected an expired token to fail verification")
	}
}

func TestLocalePreferencesRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	locale, err := GetLocale(ctx, pool, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if locale != DefaultLocale {
		t.Fatalf("expected default locale %q, got %q", DefaultLocale, locale)
	}

	if err := SetLocale(ctx, pool, tenantID, userID, "vi"); err != nil {
		t.Fatal(err)
	}
	locale, err = GetLocale(ctx, pool, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if locale != "vi" {
		t.Fatalf("expected vi, got %q", locale)
	}

	// Setting it again (an upsert, not a duplicate-key error) changes it in place.
	if err := SetLocale(ctx, pool, tenantID, userID, "en"); err != nil {
		t.Fatal(err)
	}
	locale, err = GetLocale(ctx, pool, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if locale != "en" {
		t.Fatalf("expected en, got %q", locale)
	}

	pool.Exec(ctx, "DELETE FROM user_preferences WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
}

func TestFetchContextAttributesReadsTheCallersOwnRecordByUserID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	// A dedicated, schema-qualified table — created ad hoc for this test, same convention as
	// every other e2e test in this port that needs its own fixture table rather than depending
	// on a specific business entity being registered.
	tableName := "public.test_context_attrs_" + fastUUIDSuffix()
	if _, err := pool.Exec(ctx, "CREATE TABLE "+tableName+
		" (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, deleted boolean NOT NULL DEFAULT false, data jsonb NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName) })

	recordID := uuid.New()
	data := `{"userId": "` + userID.String() + `", "department": "security"}`
	if _, err := pool.Exec(ctx, "INSERT INTO "+tableName+" (id, tenant_id, data) VALUES ($1, $2, $3::jsonb)",
		recordID, tenantID, data); err != nil {
		t.Fatal(err)
	}

	attrs, err := FetchContextAttributes(ctx, pool, tenantID, tableName, userID)
	if err != nil {
		t.Fatal(err)
	}
	if attrs == nil || attrs["department"] != "security" {
		t.Fatalf("expected department=security, got %v", attrs)
	}

	// A different user id must find nothing.
	none, err := FetchContextAttributes(ctx, pool, tenantID, tableName, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("expected no match for an unrelated user id, got %v", none)
	}

	// A soft-deleted row must not be returned even if the userId matches.
	if _, err := pool.Exec(ctx, "UPDATE "+tableName+" SET deleted = true WHERE id = $1", recordID); err != nil {
		t.Fatal(err)
	}
	deleted, err := FetchContextAttributes(ctx, pool, tenantID, tableName, userID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("expected a soft-deleted row to be excluded, got %v", deleted)
	}
}

func fastUUIDSuffix() string {
	id := uuid.New().String()
	return "t" + id[:8]
}
