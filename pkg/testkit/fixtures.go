package testkit

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RedisURL returns REDIS_URL, failing the test when it is unset.
func RedisURL(t testing.TB) string {
	t.Helper()
	v := os.Getenv("REDIS_URL")
	if v == "" {
		t.Fatal("testkit: REDIS_URL is not set (e.g. redis://127.0.0.1:6379)")
	}
	return v
}

// SeedRole grants role to userID within tenantID (metadata.user_roles) — what most tests need
// before minting a token, since permission checks are deny-by-default.
func SeedRole(t testing.TB, pool *pgxpool.Pool, tenantID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)`,
		tenantID, userID, role); err != nil {
		t.Fatalf("testkit: seeding role %q for user %s: %v", role, userID, err)
	}
}
