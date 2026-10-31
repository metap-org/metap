package oauthserver

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RefreshTokenTTLSeconds: 30 days — no config surface for this in v1, long enough that a real
// integration doesn't need to re-run the authorization-code dance constantly, short enough that
// a refresh token that's simply never used again eventually stops being a standing credential.
const RefreshTokenTTLSeconds = 30 * 24 * 3600

type RefreshToken struct {
	ID       uuid.UUID
	ClientID uuid.UUID
	TenantID uuid.UUID
	UserID   uuid.UUID
	Scope    string
}

type CreateRefreshInput struct {
	ClientID uuid.UUID
	TenantID uuid.UUID
	UserID   uuid.UUID
	Scope    string
}

// ConsumeRefreshOutcomeKind distinguishes what presenting a refresh token to the (not yet
// ported) token endpoint's refresh_token grant resolved to.
type ConsumeRefreshOutcomeKind int

const (
	// RefreshRotated: the token was live; it's now revoked and replaced by the returned fresh
	// one — the caller mints a new access token from the returned record's fields and returns
	// RawToken to the client as the new refresh_token.
	RefreshRotated ConsumeRefreshOutcomeKind = iota
	// RefreshInvalid: no matching, unrevoked, unexpired row for this (token, clientID) — a
	// wrong/garbage token, a client mismatch, or one that's simply expired. The caller responds
	// invalid_grant; deliberately not distinguished further, same reasoning verify-credentials
	// style logic gives for not telling a caller why a login failed.
	RefreshInvalid
	// RefreshReused: the presented token was already consumed by an earlier rotation — only
	// possible if it was captured and replayed by a second party after the legitimate holder
	// already rotated past it (or a legitimate caller retried after losing the rotated response,
	// an accepted false-positive this package treats identically, matching standard
	// refresh-token-rotation guidance: fail closed rather than try to distinguish the two). The
	// entire live chain for this (ClientID, UserID) has already been revoked by the time this
	// outcome is returned.
	RefreshReused
)

// ConsumeRefreshOutcome is the result of ConsumeRefreshToken. Record/RawToken are only populated
// for Kind == RefreshRotated.
type ConsumeRefreshOutcome struct {
	Kind     ConsumeRefreshOutcomeKind
	Record   *RefreshToken
	RawToken string
}

func scanRefreshToken(row pgx.Row) (*RefreshToken, error) {
	var r RefreshToken
	if err := row.Scan(&r.ID, &r.ClientID, &r.TenantID, &r.UserID, &r.Scope); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRefreshToken is issued right after a successful authorization_code exchange, and again
// on every ConsumeRefreshToken rotation — never any other time.
func CreateRefreshToken(ctx context.Context, tx pgx.Tx, input CreateRefreshInput) (*RefreshToken, string, error) {
	rawToken, err := GenerateOpaqueToken()
	if err != nil {
		return nil, "", err
	}
	expiresAt := time.Now().UTC().Add(RefreshTokenTTLSeconds * time.Second)

	row := tx.QueryRow(ctx,
		`INSERT INTO oauth_refresh_tokens (token_hash, client_id, tenant_id, user_id, scope, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, client_id, tenant_id, user_id, scope`,
		HashToken(rawToken), input.ClientID, input.TenantID, input.UserID, input.Scope, expiresAt,
	)
	record, err := scanRefreshToken(row)
	if err != nil {
		return nil, "", err
	}
	return record, rawToken, nil
}

// ConsumeRefreshToken takes an open transaction (not a generic DBTX) because rotation genuinely
// needs multiple statements to succeed or fail together: a caller who fails to mint the new
// access token after this returns RefreshRotated must roll the whole thing back, or the client
// would be left holding a rotated refresh token with no access token to show for it. The caller
// (the not-yet-ported oauth2 HTTP routes) commits only after successfully minting the new access
// token JWT.
//
// Reuse detection: a presented token whose row is already revoked did not come from the
// legitimate holder's last response (they'd have the *new* token from that rotation, not this
// one) — this is exactly the signature of a stolen-and-replayed refresh token. Every other live
// (unrevoked, unexpired) token for this (clientID, userID) pair is revoked immediately, cutting
// off whichever party — attacker or legitimate holder, this function can't tell which — is still
// holding a token from the same compromised chain. A real integration should treat RefreshReused
// as "re-run the authorization_code flow from scratch", not retry the refresh.
func ConsumeRefreshToken(ctx context.Context, tx pgx.Tx, rawToken string, clientID uuid.UUID) (*ConsumeRefreshOutcome, error) {
	tokenHash := HashToken(rawToken)

	row := tx.QueryRow(ctx,
		`SELECT id, client_id, tenant_id, user_id, scope, revoked_at, expires_at
		 FROM oauth_refresh_tokens WHERE token_hash = $1 AND client_id = $2`,
		tokenHash, clientID,
	)
	var id, rowClientID, tenantID, userID uuid.UUID
	var scope string
	var revokedAt *time.Time
	var expiresAt time.Time
	if err := row.Scan(&id, &rowClientID, &tenantID, &userID, &scope, &revokedAt, &expiresAt); err != nil {
		if err == pgx.ErrNoRows {
			return &ConsumeRefreshOutcome{Kind: RefreshInvalid}, nil
		}
		return nil, err
	}

	if revokedAt != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE oauth_refresh_tokens SET revoked_at = now()
			 WHERE client_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			clientID, userID,
		); err != nil {
			return nil, err
		}
		return &ConsumeRefreshOutcome{Kind: RefreshReused}, nil
	}

	if !expiresAt.After(time.Now().UTC()) {
		return &ConsumeRefreshOutcome{Kind: RefreshInvalid}, nil
	}

	newRecord, rawNewToken, err := CreateRefreshToken(ctx, tx, CreateRefreshInput{
		ClientID: clientID,
		TenantID: tenantID,
		UserID:   userID,
		Scope:    scope,
	})
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE oauth_refresh_tokens SET revoked_at = now(), replaced_by = $1 WHERE id = $2`,
		newRecord.ID, id,
	); err != nil {
		return nil, err
	}

	return &ConsumeRefreshOutcome{Kind: RefreshRotated, Record: newRecord, RawToken: rawNewToken}, nil
}

// RevokeRefreshToken (RFC 7009) is the refresh-token half of revocation — see this package's own
// doc comment for why an already-issued access token can't be revoked the same way. Silent no-op
// if nothing matches (wrong client, unknown token, already revoked): RFC 7009 §2.2 treats an
// unrecognized/already-invalid token as success, so a caller can't use this endpoint's response
// to probe whether a guessed token was ever valid.
func RevokeRefreshToken(ctx context.Context, db DBTX, rawToken string, clientID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE oauth_refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND client_id = $2 AND revoked_at IS NULL`,
		HashToken(rawToken), clientID,
	)
	return err
}
