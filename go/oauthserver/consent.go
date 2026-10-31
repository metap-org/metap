// Real interactive consent for the (not yet ported) authorize endpoint — before this existed,
// any already-authenticated caller silently "approved" any registered client's requested scope
// with no screen shown at all. See migrations/0035_oauth2_consent.sql for the two tables
// this owns and why they're shaped the way they are; metap-http's oauth2 routes (not yet ported)
// render the actual consent screen, since this package has no HTTP of its own.
package oauthserver

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PendingAuthorizationTTLSeconds: 10 minutes — long enough for a real person to read the screen
// and click, short enough that an abandoned consent screen (tab closed, never decided) doesn't
// linger.
const PendingAuthorizationTTLSeconds = 10 * 60

type PendingAuthorization struct {
	ID                  uuid.UUID
	ClientID            uuid.UUID
	TenantID            uuid.UUID
	UserID              uuid.UUID
	RedirectURI         string
	Scope               string
	CodeChallenge       *string
	CodeChallengeMethod *string
	ClientState         *string
}

type CreatePendingAuthorizationInput struct {
	ClientID            uuid.UUID
	TenantID            uuid.UUID
	UserID              uuid.UUID
	RedirectURI         string
	Scope               string
	CodeChallenge       *string
	CodeChallengeMethod *string
	ClientState         *string
}

func scanPending(row pgx.Row) (*PendingAuthorization, error) {
	var p PendingAuthorization
	if err := row.Scan(&p.ID, &p.ClientID, &p.TenantID, &p.UserID, &p.RedirectURI, &p.Scope,
		&p.CodeChallenge, &p.CodeChallengeMethod, &p.ClientState); err != nil {
		return nil, err
	}
	return &p, nil
}

const pendingColumns = `id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method,
	client_state`

// CreatePendingAuthorization persists the full, already-validated authorize request so the
// consent screen's approve/deny POST never has to re-trust caller-supplied query params a second
// time — it only ever reads back what this row already validated when the screen was first
// shown.
func CreatePendingAuthorization(ctx context.Context, db DBTX, input CreatePendingAuthorizationInput) (*PendingAuthorization, error) {
	expiresAt := time.Now().UTC().Add(PendingAuthorizationTTLSeconds * time.Second)
	row := db.QueryRow(ctx,
		`INSERT INTO oauth_pending_authorizations
			(client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method,
			 client_state, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+pendingColumns,
		input.ClientID, input.TenantID, input.UserID, input.RedirectURI, input.Scope, input.CodeChallenge,
		input.CodeChallengeMethod, input.ClientState, expiresAt,
	)
	return scanPending(row)
}

// GetPendingAuthorization is a read-only lookup for rendering the consent screen — does **not**
// consume the row (a page reload/back-button before deciding must still show the same screen).
// Returns (nil, nil) for an unknown or expired id; the caller turns that into a plain "this
// request has expired, start again" response rather than a 500.
func GetPendingAuthorization(ctx context.Context, db DBTX, id uuid.UUID) (*PendingAuthorization, error) {
	row := db.QueryRow(ctx,
		`SELECT `+pendingColumns+`
		 FROM oauth_pending_authorizations WHERE id = $1 AND expires_at > now()`,
		id,
	)
	p, err := scanPending(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// ConsumePendingAuthorization atomically deletes-and-returns — the approve/deny decision
// consumes the pending row exactly once, same single-use discipline ConsumeAuthorizationCode
// uses for the same reason (a double-submitted decision, e.g. a double click, must not both
// succeed).
//
// tenantID/userID are matched **in the query itself**, not checked after the fact — a mismatched
// caller (a different, legitimately-authenticated user in the same tenant probing someone else's
// pending request) must not be able to delete a row it can't actually decide, which an
// "consume unconditionally, then reject after the fact" version would do (destroying the real
// owner's chance to ever approve it — a self-inflicted denial of service, not a data leak, but
// avoidable at zero extra cost here).
//
// Returns (nil, nil) when no row matches.
func ConsumePendingAuthorization(ctx context.Context, db DBTX, id, tenantID, userID uuid.UUID) (*PendingAuthorization, error) {
	row := db.QueryRow(ctx,
		`DELETE FROM oauth_pending_authorizations
		 WHERE id = $1 AND tenant_id = $2 AND user_id = $3 AND expires_at > now()
		 RETURNING `+pendingColumns,
		id, tenantID, userID,
	)
	p, err := scanPending(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// GetConsentScope is the scope a (client, user) pair has already approved, if any — the
// authorize flow skips the consent screen entirely when the newly requested scope is a subset of
// this (the standard "you've already granted this app access" IdP behavior), re-prompting only
// when the client asks for something new. Returns "" when no consent row exists (distinguished
// by the bool return).
func GetConsentScope(ctx context.Context, db DBTX, clientID, userID uuid.UUID) (string, bool, error) {
	var scope string
	err := db.QueryRow(ctx, `SELECT scope FROM oauth_consents WHERE client_id = $1 AND user_id = $2`, clientID, userID).
		Scan(&scope)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return scope, true, nil
}

// RecordConsent records an approval — widens the stored scope to the union of whatever was
// already granted plus this approval, rather than replacing it, so approving a *narrower*
// re-request (a client that happens to ask for less this time) never silently shrinks a
// previously wider grant.
//
// db must be safe to use twice without an open transaction (every real caller passes the pool
// directly) — this needs 2 statements (read the prior scope, then upsert the merged one), and
// doing so without a transaction is fine since there is no atomicity requirement across the two
// (a lost update here just means a consent screen is shown once more than strictly necessary).
func RecordConsent(ctx context.Context, db DBTX, tenantID, clientID, userID uuid.UUID, newlyApprovedScope string) error {
	existing, ok, err := GetConsentScope(ctx, db, clientID, userID)
	if err != nil {
		return err
	}

	merged := newlyApprovedScope
	if ok {
		tokens := ScopeTokens(existing)
		seen := make(map[string]bool, len(tokens))
		for _, t := range tokens {
			seen[t] = true
		}
		for _, t := range ScopeTokens(newlyApprovedScope) {
			if !seen[t] {
				tokens = append(tokens, t)
				seen[t] = true
			}
		}
		merged = strings.Join(tokens, " ")
	}

	_, err = db.Exec(ctx,
		`INSERT INTO oauth_consents (client_id, tenant_id, user_id, scope) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (client_id, user_id) DO UPDATE SET scope = $4, updated_at = now()`,
		clientID, tenantID, userID, merged,
	)
	return err
}
