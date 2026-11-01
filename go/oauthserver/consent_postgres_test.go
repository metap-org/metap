package oauthserver

// The Rust metap-oauth-server crate's consent.rs has zero tests of its own, unit or e2e
// (confirmed by reading its source and the crate's tests/ directory) — this is new coverage
// added during the port, verified against a real Postgres rather than mocked, matching this
// project's own "verify sống" convention.

import (
	"context"
	"testing"

	"github.com/google/uuid"
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
	if err != nil {
		t.Fatal(err)
	}

	// A reload before deciding must still show the same screen — GetPendingAuthorization does
	// not consume.
	again, err := GetPendingAuthorization(ctx, pool, pending.ID)
	if err != nil || again == nil {
		t.Fatalf("expected the pending authorization to still be readable, err=%v", err)
	}

	first, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	if err != nil || first == nil {
		t.Fatalf("expected the first consume to succeed, err=%v", err)
	}

	second, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatal("a second consume of the same pending authorization must fail")
	}
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
	if err != nil {
		t.Fatal(err)
	}

	wrongUser, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, otherUserID)
	if err != nil {
		t.Fatal(err)
	}
	if wrongUser != nil {
		t.Fatal("a different user must not be able to consume someone else's pending authorization")
	}

	// The real owner can still decide it — the mismatched attempt must not have burned the row.
	rightUser, err := ConsumePendingAuthorization(ctx, pool, pending.ID, tenantID, userID)
	if err != nil || rightUser == nil {
		t.Fatalf("expected the real owner to still be able to consume it, err=%v", err)
	}
}

func TestConsentWidensRatherThanReplacesTheGrantedScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	client, _ := makeConfidentialClient(t, pool, tenantID)

	if _, ok, err := GetConsentScope(ctx, pool, client.ID, userID); err != nil || ok {
		t.Fatalf("expected no prior consent, ok=%v err=%v", ok, err)
	}

	if err := RecordConsent(ctx, pool, tenantID, client.ID, userID, "read:widgets"); err != nil {
		t.Fatal(err)
	}
	scope, ok, err := GetConsentScope(ctx, pool, client.ID, userID)
	if err != nil || !ok || scope != "read:widgets" {
		t.Fatalf("expected scope=read:widgets, got %q ok=%v err=%v", scope, ok, err)
	}

	// Approving a *narrower* re-request must not shrink the previously wider grant.
	if err := RecordConsent(ctx, pool, tenantID, client.ID, userID, "read:widgets"); err != nil {
		t.Fatal(err)
	}
	// Approving something new widens the union.
	if err := RecordConsent(ctx, pool, tenantID, client.ID, userID, "write:widgets"); err != nil {
		t.Fatal(err)
	}
	scope, ok, err = GetConsentScope(ctx, pool, client.ID, userID)
	if err != nil || !ok {
		t.Fatalf("expected a consent row, ok=%v err=%v", ok, err)
	}
	if !ScopeIsSubset("read:widgets write:widgets", scope) || !ScopeIsSubset(scope, "read:widgets write:widgets") {
		t.Fatalf("expected the merged scope to be exactly {read:widgets, write:widgets}, got %q", scope)
	}
}
