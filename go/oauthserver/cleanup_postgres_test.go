package oauthserver

// Live-verifies DeleteExpired actually removes stale rows and leaves live ones alone — can't be
// proven against a mock since the whole point is the WHERE clause's interaction with real
// expires_at/revoked_at timestamps. Ported from the Rust metap-oauth-server crate's
// tests/cleanup_postgres.rs.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Both tests sweep the *whole* table, so run concurrently one's sweep deletes the other's stale
// rows before it can count them. Serialising the two keeps each test's counts about its own rows.
var cleanupSweepLock sync.Mutex

func makeCleanupTestClient(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) uuid.UUID {
	serviceUserID := makeTestUser(t, pool, tenantID)
	client, _, err := CreateClient(context.Background(), pool, CreateClientInput{
		TenantID:       tenantID,
		Name:           "Cleanup Test Client",
		RedirectURIs:   []string{"https://example.com/callback"},
		AllowedScopes:  []string{"read:widgets"},
		IsConfidential: true,
		ServiceUserID:  serviceUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.ID
}

func insertCode(t *testing.T, pool *pgxpool.Pool, clientID, tenantID uuid.UUID, expiresAt time.Time) uuid.UUID {
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO oauth_authorization_codes
			(code_hash, client_id, tenant_id, user_id, redirect_uri, scope, expires_at)
		 VALUES ($1, $2, $3, $4, 'https://example.com/callback', '', $5)
		 RETURNING id`,
		uuid.NewString(), clientID, tenantID, uuid.New(), expiresAt,
	).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRefreshToken(t *testing.T, pool *pgxpool.Pool, clientID, tenantID uuid.UUID, expiresAt time.Time, revokedAt *time.Time) uuid.UUID {
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO oauth_refresh_tokens (token_hash, client_id, tenant_id, user_id, scope, expires_at, revoked_at)
		 VALUES ($1, $2, $3, $4, '', $5, $6)
		 RETURNING id`,
		uuid.NewString(), clientID, tenantID, uuid.New(), expiresAt, revokedAt,
	).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rowExists(t *testing.T, pool *pgxpool.Pool, table string, id uuid.UUID) bool {
	var count int64
	err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE id = $1", id).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func TestDeletesOnlyRowsPastTheirGracePeriod(t *testing.T) {
	cleanupSweepLock.Lock()
	defer cleanupSweepLock.Unlock()
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	clientID := makeCleanupTestClient(t, pool, tenantID)
	now := time.Now().UTC()

	// Well past the 24h grace period on both sides of the sweep's OR clause.
	longExpiredCode := insertCode(t, pool, clientID, tenantID, now.Add(-48*time.Hour))
	longExpiredToken := insertRefreshToken(t, pool, clientID, tenantID, now.Add(-48*time.Hour), nil)
	longRevokedAt := now.Add(-48 * time.Hour)
	longRevokedToken := insertRefreshToken(t, pool, clientID, tenantID, now.Add(10*24*time.Hour), &longRevokedAt)

	// Still live — neither expired nor revoked.
	liveCode := insertCode(t, pool, clientID, tenantID, now.Add(60*time.Second))
	liveToken := insertRefreshToken(t, pool, clientID, tenantID, now.Add(30*24*time.Hour), nil)

	counts, err := DeleteExpired(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if counts.AuthorizationCodesDeleted < 1 {
		t.Fatalf("expected at least 1 code deleted, got %+v", counts)
	}
	if counts.RefreshTokensDeleted < 2 {
		t.Fatalf("expected at least 2 tokens deleted, got %+v", counts)
	}

	if rowExists(t, pool, "oauth_authorization_codes", longExpiredCode) {
		t.Fatal("expected long-expired code to be deleted")
	}
	if rowExists(t, pool, "oauth_refresh_tokens", longExpiredToken) {
		t.Fatal("expected long-expired token to be deleted")
	}
	if rowExists(t, pool, "oauth_refresh_tokens", longRevokedToken) {
		t.Fatal("expected long-revoked token to be deleted")
	}

	if !rowExists(t, pool, "oauth_authorization_codes", liveCode) {
		t.Fatal("expected live code to survive")
	}
	if !rowExists(t, pool, "oauth_refresh_tokens", liveToken) {
		t.Fatal("expected live token to survive")
	}

	// Cleanup this test's own live leftovers so a re-run of the suite starts clean.
	pool.Exec(ctx, "DELETE FROM oauth_authorization_codes WHERE id = $1", liveCode)
	pool.Exec(ctx, "DELETE FROM oauth_refresh_tokens WHERE id = $1", liveToken)
}

// With a batch size far below the stale-row count the sweep must loop until nothing stale is
// left — 6 codes (an exact multiple of the batch, so the last statement deletes 0) and 7 tokens
// (a remainder, so the last statement deletes 1) — and leave live rows alone. A
// single-statement implementation would pass TestDeletesOnlyRowsPastTheirGracePeriod but not
// this.
func TestSweepsEveryStaleRowAcrossMultipleBatches(t *testing.T) {
	cleanupSweepLock.Lock()
	defer cleanupSweepLock.Unlock()
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	clientID := makeCleanupTestClient(t, pool, tenantID)
	now := time.Now().UTC()

	var staleCodes []uuid.UUID
	for i := 0; i < 6; i++ {
		staleCodes = append(staleCodes, insertCode(t, pool, clientID, tenantID, now.Add(-72*time.Hour)))
	}
	var staleTokens []uuid.UUID
	for i := 0; i < 7; i++ {
		staleTokens = append(staleTokens, insertRefreshToken(t, pool, clientID, tenantID, now.Add(-72*time.Hour), nil))
	}
	liveCode := insertCode(t, pool, clientID, tenantID, now.Add(5*time.Minute))
	liveToken := insertRefreshToken(t, pool, clientID, tenantID, now.Add(30*24*time.Hour), nil)

	counts, err := DeleteExpiredInBatches(ctx, pool, 3)
	if err != nil {
		t.Fatal(err)
	}
	if counts.AuthorizationCodesDeleted < 6 {
		t.Fatalf("expected at least 6 codes deleted, got %+v", counts)
	}
	if counts.RefreshTokensDeleted < 7 {
		t.Fatalf("expected at least 7 tokens deleted, got %+v", counts)
	}

	for _, id := range staleCodes {
		if rowExists(t, pool, "oauth_authorization_codes", id) {
			t.Fatalf("stale code %s survived", id)
		}
	}
	for _, id := range staleTokens {
		if rowExists(t, pool, "oauth_refresh_tokens", id) {
			t.Fatalf("stale token %s survived", id)
		}
	}
	if !rowExists(t, pool, "oauth_authorization_codes", liveCode) {
		t.Fatal("expected live code to survive")
	}
	if !rowExists(t, pool, "oauth_refresh_tokens", liveToken) {
		t.Fatal("expected live token to survive")
	}

	pool.Exec(ctx, "DELETE FROM oauth_authorization_codes WHERE id = $1", liveCode)
	pool.Exec(ctx, "DELETE FROM oauth_refresh_tokens WHERE id = $1", liveToken)
}
