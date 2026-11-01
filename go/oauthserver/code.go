package oauthserver

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuthorizationCodeTTLSeconds: RFC 6749 doesn't mandate a lifetime; 60s is the value most real
// IdPs converge on — long enough for a browser redirect round trip, short enough that a code
// leaked into a server log or Referer header is worthless by the time anyone could act on it.
const AuthorizationCodeTTLSeconds = 60

type AuthorizationCode struct {
	ID                  uuid.UUID
	ClientID            uuid.UUID
	TenantID            uuid.UUID
	UserID              uuid.UUID
	RedirectURI         string
	Scope               string
	CodeChallenge       *string
	CodeChallengeMethod *string
}

type CreateCodeInput struct {
	ClientID            uuid.UUID
	TenantID            uuid.UUID
	UserID              uuid.UUID
	RedirectURI         string
	Scope               string
	CodeChallenge       *string
	CodeChallengeMethod *string
}

func scanCode(row pgx.Row) (*AuthorizationCode, error) {
	var c AuthorizationCode
	if err := row.Scan(&c.ID, &c.ClientID, &c.TenantID, &c.UserID, &c.RedirectURI, &c.Scope,
		&c.CodeChallenge, &c.CodeChallengeMethod); err != nil {
		return nil, err
	}
	return &c, nil
}

const codeColumns = `id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method`

// CreateAuthorizationCode is issued by the (not yet ported) authorize endpoint once the caller
// (an already-authenticated session) is treated as approving the client's request. Returns
// (row, rawCode): rawCode is what actually goes on the redirect URL, never persisted — only its
// hash is.
func CreateAuthorizationCode(ctx context.Context, db DBTX, input CreateCodeInput) (*AuthorizationCode, string, error) {
	rawCode, err := GenerateOpaqueToken()
	if err != nil {
		return nil, "", err
	}
	expiresAt := time.Now().UTC().Add(AuthorizationCodeTTLSeconds * time.Second)

	row := db.QueryRow(ctx,
		`INSERT INTO oauth_authorization_codes
			(code_hash, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge,
			 code_challenge_method, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+codeColumns,
		HashToken(rawCode), input.ClientID, input.TenantID, input.UserID, input.RedirectURI, input.Scope,
		input.CodeChallenge, input.CodeChallengeMethod, expiresAt,
	)
	code, err := scanCode(row)
	if err != nil {
		return nil, "", err
	}
	return code, rawCode, nil
}

// ConsumeAuthorizationCode atomically marks the code used and returns it — used_at IS NULL AND
// expires_at > now() in the same UPDATE that sets used_at, so two concurrent redemption attempts
// (a real replay, or a client retrying a timed-out request) can never both succeed: exactly one
// UPDATE matches the row, the other sees used_at already set.
//
// redirectURI is checked here too (RFC 6749 §4.1.3: the token request's redirect_uri must
// exactly match the one used to obtain the code) — folded into the same atomic query rather than
// compared after the fact, so a mismatched redirect can't itself burn a code that a legitimate
// retry with the correct redirect_uri might otherwise still need.
//
// PKCE verification is not done here — this only returns the stored code_challenge/
// code_challenge_method for the caller to check against a presented code_verifier via VerifyPKCE.
// The code is already consumed by the time that check runs, which is correct: a code is
// single-use regardless of whether the PKCE check that follows passes.
//
// Returns (nil, nil) when no row matches.
func ConsumeAuthorizationCode(ctx context.Context, db DBTX, rawCode string, clientID uuid.UUID, redirectURI string) (*AuthorizationCode, error) {
	row := db.QueryRow(ctx,
		`UPDATE oauth_authorization_codes SET used_at = now()
		 WHERE code_hash = $1 AND client_id = $2 AND redirect_uri = $3
		   AND used_at IS NULL AND expires_at > now()
		 RETURNING `+codeColumns,
		HashToken(rawCode), clientID, redirectURI,
	)
	code, err := scanCode(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return code, nil
}
