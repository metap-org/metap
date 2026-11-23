// Package oauthserver is metap acting as an OAuth2 Authorization Server (RFC 6749) — issues tokens
// to a third-party client acting on behalf of a tenant's own user. No HTTP, no business-entity
// knowledge here — the same shape as the cron/dashboards packages; metap-http's own oauth2 routes
// are the HTTP surface built on this.
// Scope shipped: authorization_code (+ mandatory-for-public-clients PKCE, S256 only) and
// refresh_token grants, plus interactive consent. client_credentials (RFC 6749 §4.4) is provisioned
// for at the schema level.
package oauthserver

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
