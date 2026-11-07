//go:build integration

package oauthserver

// The Rust metap-oauth-server crate's consent.rs has zero tests of its own, unit or e2e
// (confirmed by reading its source and the crate's tests/ directory) — this is new coverage
// added during the port, verified against a real Postgres rather than mocked, matching this
// project's own "verify sống" convention.

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPendingAuthorizationIsConsumedExactlyOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	pending, err := CreatePendingAuthorization(ctx, pool, CreatePendingAuthorizationInput{
		ClientID:    client.ID,
		TenantID:    tenantID,
		UserID:      userID,
		RedirectURI: "https://example.com/callback",
		Scope:       "read:widgets",
	})
	require.NoError(t, err)

	// A reload before deciding must still show the same screen — GetPendingAuthorization does
	// not consume.
	again, err := GetPendingAuthorization(ctx, pool, pending.ID)
	require.NoError(t, err, "expected the pending authorization to still be readable, err=")
	require.NotNil(t, again, "expected the pending authorization to still be readable, err=%v", err)

	first, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	require.NoError(t, err, "expected the first consume to succeed, err=")
	require.NotNil(t, first, "expected the first consume to succeed, err=%v", err)

	second, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	require.NoError(t, err)
	require.Nil(t, second, "a second consume of the same pending authorization must fail")
}

func TestConsumePendingAuthorizationRejectsAMismatchedUser(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	pending, err := CreatePendingAuthorization(ctx, pool, CreatePendingAuthorizationInput{
		ClientID:    client.ID,
		TenantID:    tenantID,
		UserID:      userID,
		RedirectURI: "https://example.com/callback",
	})
	require.NoError(t, err)

	wrongUser, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, otherUserID)
	require.NoError(t, err)
	require.Nil(t, wrongUser, "a different user must not be able to consume someone else's pending authorization")

	// The real owner can still decide it — the mismatched attempt must not have burned the row.
	rightUser, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	require.NoError(t, err, "expected the real owner to still be able to consume it, err=")
	require.NotNil(t, rightUser, "expected the real owner to still be able to consume it, err=%v", err)
}

func TestConsentWidensRatherThanReplacesTheGrantedScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	if _, ok, err := GetConsentScope(ctx, pool, client.ID, userID); err != nil || ok {
		require.FailNow(t, fmt.Sprintf("expected no prior consent, ok=%v err=%v", ok, err))
	}

	require.NoError(t, RecordConsent(ctx, pool, tenantID, client.ID, userID, "read:widgets"))
	scope, ok, err := GetConsentScope(ctx, pool, client.ID, userID)
	require.NoError(t, err, "expected scope=read:widgets, got %q ok=%v err=", scope, ok)
	require.True(t, ok, "expected scope=read:widgets, got %q ok=%v err=%v", scope, ok, err)
	require.Equal(t, "read:widgets", scope, "expected scope=read:widgets, got %q ok=%v err=%v", scope, ok, err)

	// Approving a *narrower* re-request must not shrink the previously wider grant.
	require.NoError(t, RecordConsent(ctx, pool, tenantID, client.ID, userID, "read:widgets"))
	// Approving something new widens the union.
	require.NoError(t, RecordConsent(ctx, pool, tenantID, client.ID, userID, "write:widgets"))
	scope, ok, err = GetConsentScope(ctx, pool, client.ID, userID)
	require.NoError(t, err, "expected a consent row, ok=%v err=", ok)
	require.True(t, ok, "expected a consent row, ok=%v err=%v", ok, err)
	require.True(t, ScopeIsSubset("read:widgets write:widgets", scope), "expected the merged scope to be exactly {read:widgets, write:widgets}, got %q", scope)
	require.True(t, ScopeIsSubset(scope, "read:widgets write:widgets"), "expected the merged scope to be exactly {read:widgets, write:widgets}, got %q", scope)
}
