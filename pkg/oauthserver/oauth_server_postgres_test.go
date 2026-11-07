//go:build integration

package oauthserver

// Live-verifies the full authorization_code -> access/refresh token -> rotation ->
// reuse-detection -> revoke lifecycle against a real Postgres, since none of client.go/code.go/
// refresh.go's atomicity guarantees (single-use code, refresh-token rotation, reuse detection)
// can be proven against a mock — ported from the Rust metap-oauth-server crate's
// tests/oauth_server_postgres.rs.

import (
	"context"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
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
	require.NoError(t, err)
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
	require.NoError(t, err)
	return client, secret
}

func TestClientSecretIsNeverStoredInPlaintextAndVerifiesCorrectly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	client, secret := makeConfidentialClient(t, pool, tenantID)

	lookedUp, err := GetClientByClientID(ctx, pool, client.ClientID)
	require.NoError(t, err)
	require.NotNil(t, lookedUp, err)
	require.NotEqual(t, secret, lookedUp.ClientSecretHash, "the raw secret must never be the stored value")
	require.True(t, VerifyClientSecret(lookedUp, secret), "expected the real secret to verify")
	require.False(t, VerifyClientSecret(lookedUp, "wrong-secret"), "expected a wrong secret to fail")
}

func TestRevokedClientFailsSecretVerificationEvenWithTheRightSecret(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	client, secret := makeConfidentialClient(t, pool, tenantID)

	revoked, err := RevokeClient(ctx, pool, tenantID, client.ID)
	require.NoError(t, err, "expected revoke to succeed, err=%v revoked=%v", err, revoked)
	require.True(t, revoked, "expected revoke to succeed, err=%v revoked=%v", err, revoked)
	lookedUp, err := GetClientByClientID(ctx, pool, client.ClientID)
	require.NoError(t, err)
	require.NotNil(t, lookedUp, err)
	require.False(t, VerifyClientSecret(lookedUp, secret), "expected a revoked client to fail secret verification")
	// A second revoke of an already-revoked client reports "nothing changed", not an error.
	revokedAgain, err := RevokeClient(ctx, pool, tenantID, client.ID)
	require.NoError(t, err)
	require.False(t, revokedAgain, "expected a second revoke to report false")
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
	require.NoError(t, err)

	first, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, redirectURI)
	require.NoError(t, err)
	require.NotNil(t, first, "first redemption must succeed")

	second, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, redirectURI)
	require.NoError(t, err)
	require.Nil(t, second, "a second redemption of the same code must fail — this is the replay this test exists to rule out")
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
	require.NoError(t, err)

	wrong, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, "https://attacker.example/callback")
	require.NoError(t, err)
	require.Nil(t, wrong, "RFC 6749 §4.1.3: redirect_uri must match exactly what /authorize used")

	// The code is still live for the *correct* redirect_uri — a mismatched attempt must not burn it.
	correct, err := ConsumeAuthorizationCode(ctx, pool, rawCode, client.ID, "https://example.com/callback")
	require.NoError(t, err)
	require.NotNil(t, correct, "expected the correct redirect_uri to still redeem the code")
}

func TestRefreshTokenRotatesAndTheOldOneStopsWorking(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, rawRefresh, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{
		ClientID: client.ID,
		TenantID: tenantID,
		UserID:   userID,
		Scope:    "read:widgets",
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	outcome, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, RefreshRotated, outcome.Kind, "expected RefreshRotated, got %v", outcome.Kind)
	require.Equal(t, "read:widgets", outcome.Record.Scope, "expected scope to carry over, got %q", outcome.Record.Scope)
	require.NotEqual(t, rawRefresh, outcome.RawToken, "expected a genuinely new raw token")

	// The old token is now dead — confirmed via Reused below, not Invalid, since it was consumed
	// (not merely expired/never-existed).
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	replay, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, RefreshReused, replay.Kind, "expected RefreshReused, got %v", replay.Kind)
}

func TestRefreshTokenReuseRevokesTheWholeChain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, rawR0, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{ClientID: client.ID, TenantID: tenantID, UserID: userID})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// Legitimate rotation: r0 -> r1.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	rotated, err := ConsumeRefreshToken(ctx, tx, rawR0, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, RefreshRotated, rotated.Kind, "expected RefreshRotated, got %v", rotated.Kind)
	rawR1 := rotated.RawToken

	// An attacker who captured r0 replays it — this must revoke r1 too (the chain), even though
	// r1 was never itself presented improperly.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	replay, err := ConsumeRefreshToken(ctx, tx, rawR0, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, RefreshReused, replay.Kind, "expected RefreshReused, got %v", replay.Kind)

	// The legitimate holder's r1 (obtained before the replay) is now unusable too.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	legitAfterReuse, err := ConsumeRefreshToken(ctx, tx, rawR1, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.False(t, legitAfterReuse.Kind != RefreshInvalid && legitAfterReuse.Kind != RefreshReused, "r1 must be dead once reuse was detected on its predecessor")
}

func TestRevokeRefreshTokenStopsItFromRotating(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, rawRefresh, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{ClientID: client.ID, TenantID: tenantID, UserID: userID})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	require.NoError(t, RevokeRefreshToken(ctx, pool, rawRefresh, client.ID))

	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	outcome, err := ConsumeRefreshToken(ctx, tx, rawRefresh, client.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, RefreshReused, outcome.Kind, "an explicitly revoked token must read back as already-consumed")
}
