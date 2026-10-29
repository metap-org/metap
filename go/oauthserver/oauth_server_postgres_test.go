package oauthserver

// Live-verifies the full authorization_code -> access/refresh token -> rotation ->
// reuse-detection -> revoke lifecycle against a real Postgres, since none of client.go/code.go/
// refresh.go's atomicity guarantees (single-use code, refresh-token rotation, reuse detection)
// can be proven against a mock — ported from the Rust metap-oauth-server crate's
// tests/oauth_server_postgres.rs.

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// oauth_clients.service_user_id is a real FK into users — this package has no auth-provisioning
// dependency to provision one the real way (metap-http's create-client handler does that, not
// yet ported), so tests insert a minimal row directly, same as any other fixture-only user this
// codebase's e2e tests create by hand.
func makeTestUser(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) uuid.UUID {
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, 'test') RETURNING id",
		tenantID, uuid.NewString()+"@example.com",
	).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func makeConfidentialClient(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) (*OAuthClient, string) {
	serviceUserID := makeTestUser(t, pool, tenantID)
	client, secret, err := CreateClient(context.Background(), pool, CreateClientInput{
		TenantID:       tenantID,
		Name:           "Test Integration",
		RedirectURIs:   []string{"https://example.com/callback"},
		AllowedScopes:  []string{"read:widgets", "write:widgets"},
		IsConfidential: true,
		ServiceUserID:  serviceUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, secret
}

func TestClientSecretIsNeverStoredInPlaintextAndVerifiesCorrectly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	client, secret := makeConfidentialClient(t, pool, tenantID)

	lookedUp, err := GetClientByClientID(ctx, pool, client.ClientID)
	if err != nil || lookedUp == nil {
		t.Fatal(err)
	}
	if lookedUp.ClientSecretHash == secret {
		t.Fatal("the raw secret must never be the stored value")
	}
	if !VerifyClientSecret(lookedUp, secret) {
		t.Fatal("expected the real secret to verify")
	}
	if VerifyClientSecret(lookedUp, "wrong-secret") {
		t.Fatal("expected a wrong secret to fail")
	}
}

func TestRevokedClientFailsSecretVerificationEvenWithTheRightSecret(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	client, secret := makeConfidentialClient(t, pool, tenantID)

	revoked, err := RevokeClient(ctx, pool, tenantID, client.ID)
	if err != nil || !revoked {
		t.Fatalf("expected revoke to succeed, err=%v revoked=%v", err, revoked)
	}
	lookedUp, err := GetClientByClientID(ctx, pool, client.ClientID)
	if err != nil || lookedUp == nil {
		t.Fatal(err)
	}
	if VerifyClientSecret(lookedUp, secret) {
		t.Fatal("expected a revoked client to fail secret verification")
	}
	// A second revoke of an already-revoked client reports "nothing changed", not an error.
	revokedAgain, err := RevokeClient(ctx, pool, tenantID, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revokedAgain {
		t.Fatal("expected a second revoke to report false")
	}
}

func TestAuthorizationCodeIsSingleUse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)
	redirectURI := "https://example.com/callback"

	_, rawCode, err := CreateAuthorizationCode(ctx, pool, CreateCodeInput{
		ClientID:    client.ID,
		TenantID:    tenantID,
		UserID:      userID,
		RedirectURI: redirectURI,
		Scope:       "read:widgets",
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil {
		t.Fatal("first redemption must succeed")
	}

	second, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatal("a second redemption of the same code must fail — this is the replay this test exists to rule out")
	}
}

func TestAuthorizationCodeRejectsAMismatchedRedirectURI(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	_, rawCode, err := CreateAuthorizationCode(ctx, pool, CreateCodeInput{
		ClientID:    client.ID,
		TenantID:    tenantID,
		UserID:      userID,
		RedirectURI: "https://example.com/callback",
		Scope:       "",
	})
	if err != nil {
		t.Fatal(err)
	}

	wrong, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, "https://attacker.example/callback")
	if err != nil {
		t.Fatal(err)
	}
	if wrong != nil {
		t.Fatal("RFC 6749 §4.1.3: redirect_uri must match exactly what /authorize used")
	}

	// The code is still live for the *correct* redirect_uri — a mismatched attempt must not burn it.
	correct, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, "https://example.com/callback")
	if err != nil {
		t.Fatal(err)
	}
	if correct == nil {
		t.Fatal("expected the correct redirect_uri to still redeem the code")
	}
}

func TestRefreshTokenRotatesAndTheOldOneStopsWorking(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rawRefresh, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{
		ClientID: client.ID,
		TenantID: tenantID,
		UserID:   userID,
		Scope:    "read:widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != RefreshRotated {
		t.Fatalf("expected RefreshRotated, got %v", outcome.Kind)
	}
	if outcome.Record.Scope != "read:widgets" {
		t.Fatalf("expected scope to carry over, got %q", outcome.Record.Scope)
	}
	if outcome.RawToken == rawRefresh {
		t.Fatal("expected a genuinely new raw token")
	}

	// The old token is now dead — confirmed via Reused below, not Invalid, since it was consumed
	// (not merely expired/never-existed).
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if replay.Kind != RefreshReused {
		t.Fatalf("expected RefreshReused, got %v", replay.Kind)
	}
}

func TestRefreshTokenReuseRevokesTheWholeChain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rawR0, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{ClientID: client.ID, TenantID: tenantID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Legitimate rotation: r0 -> r1.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ConsumeRefreshToken(ctx, tx, rawR0, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if rotated.Kind != RefreshRotated {
		t.Fatalf("expected RefreshRotated, got %v", rotated.Kind)
	}
	rawR1 := rotated.RawToken

	// An attacker who captured r0 replays it — this must revoke r1 too (the chain), even though
	// r1 was never itself presented improperly.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ConsumeRefreshToken(ctx, tx, rawR0, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if replay.Kind != RefreshReused {
		t.Fatalf("expected RefreshReused, got %v", replay.Kind)
	}

	// The legitimate holder's r1 (obtained before the replay) is now unusable too.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legitAfterReuse, err := ConsumeRefreshToken(ctx, tx, rawR1, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if legitAfterReuse.Kind != RefreshInvalid && legitAfterReuse.Kind != RefreshReused {
		t.Fatal("r1 must be dead once reuse was detected on its predecessor")
	}
}

func TestRevokeRefreshTokenStopsItFromRotating(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rawRefresh, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{ClientID: client.ID, TenantID: tenantID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := RevokeRefreshToken(ctx, pool, rawRefresh, client.ID); err != nil {
		t.Fatal(err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != RefreshReused {
		t.Fatal("an explicitly revoked token must read back as already-consumed")
	}
}
