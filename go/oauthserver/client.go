package oauthserver

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OAuthClient is a registered third-party OAuth2 client — tenant-scoped: a client is only usable
// by the tenant that registered it, since every isolation boundary elsewhere in this platform is
// the tenant, not a global client registry every tenant shares. ClientSecretHash is intentionally
// not on this struct — see ClientWithSecret for the one place the hash is carried, and
// CreateClient's doc comment for the one place the raw secret is ever observable.
type OAuthClient struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ClientID       string
	Name           string
	RedirectURIs   []string
	AllowedScopes  []string
	IsConfidential bool
	RevokedAt      *time.Time
	// ServiceUserID is the users row this client acts as for the client_credentials grant — nil
	// only for a client registered before that grant existed (crates/migrations/
	// 0036_oauth2_client_credentials.sql's own doc comment); every client CreateClient mints today
	// gets one eagerly. Starts with zero role grants like any other principal — this column makes
	// the identity exist, it grants nothing by itself (deny-by-default, same as every other user in
	// this platform).
	ServiceUserID *uuid.UUID
}

// ClientWithSecret is what GetClientByClientID returns — carries the secret hash the token
// endpoint needs to verify client authentication against, kept off OAuthClient itself so nothing
// that only needs the public client shape (ListClients) can even accidentally touch a hash.
type ClientWithSecret struct {
	Client           OAuthClient
	ClientSecretHash string
}

type CreateClientInput struct {
	TenantID       uuid.UUID
	Name           string
	RedirectURIs   []string
	AllowedScopes  []string
	IsConfidential bool
	// ServiceUserID: the caller (eventually metap-http, not yet ported) provisions this users row
	// *before* calling CreateClient — this package has no auth-provisioning dependency and doesn't
	// know how to provision a user itself, only how to record which one a client acts as.
	ServiceUserID uuid.UUID
}

func scanClient(row pgx.Row) (*OAuthClient, error) {
	var c OAuthClient
	if err := row.Scan(&c.ID, &c.TenantID, &c.ClientID, &c.Name, &c.RedirectURIs, &c.AllowedScopes,
		&c.IsConfidential, &c.RevokedAt, &c.ServiceUserID); err != nil {
		return nil, err
	}
	return &c, nil
}

const clientColumns = `id, tenant_id, client_id, name, redirect_uris, allowed_scopes, is_confidential, revoked_at,
	service_user_id`

// CreateClient registers a new client and returns (client, clientSecret) — the only time the raw
// secret is ever available, mirroring the SecretStore's own write-only credential discipline
// elsewhere in this platform: only a hash is persisted (HashToken), so a caller who loses the
// value returned here has to rotate (revoke + recreate), not "look it up again". ClientID is a
// separate, non-secret opaque identifier (safe to log, appear in a redirect URL's query string,
// etc.) — generated the same way as the secret purely for uniform, sufficiently-random
// uniqueness, not because it needs secrecy.
func CreateClient(ctx context.Context, db DBTX, input CreateClientInput) (*OAuthClient, string, error) {
	rawID, err := GenerateOpaqueToken()
	if err != nil {
		return nil, "", err
	}
	clientID := "mcl_" + rawID
	clientSecret, err := GenerateOpaqueToken()
	if err != nil {
		return nil, "", err
	}
	clientSecretHash := HashToken(clientSecret)

	row := db.QueryRow(ctx,
		`INSERT INTO oauth_clients
			(tenant_id, client_id, client_secret_hash, name, redirect_uris, allowed_scopes, is_confidential,
			 service_user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+clientColumns,
		input.TenantID, clientID, clientSecretHash, input.Name, input.RedirectURIs, input.AllowedScopes,
		input.IsConfidential, input.ServiceUserID,
	)
	client, err := scanClient(row)
	if err != nil {
		return nil, "", err
	}
	return client, clientSecret, nil
}

// GetClientByClientID looks a client up by the wire-visible client_id (not the internal id) —
// this is what the authorize and token endpoints receive from the caller. Includes the secret
// hash so the (not yet ported) token endpoint can verify a presented client_secret without a
// second query.
func GetClientByClientID(ctx context.Context, db DBTX, clientID string) (*ClientWithSecret, error) {
	row := db.QueryRow(ctx,
		`SELECT id, tenant_id, client_id, client_secret_hash, name, redirect_uris, allowed_scopes,
				is_confidential, revoked_at, service_user_id
		 FROM oauth_clients WHERE client_id = $1`,
		clientID,
	)
	var c OAuthClient
	var secretHash string
	if err := row.Scan(&c.ID, &c.TenantID, &c.ClientID, &secretHash, &c.Name, &c.RedirectURIs, &c.AllowedScopes,
		&c.IsConfidential, &c.RevokedAt, &c.ServiceUserID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &ClientWithSecret{Client: c, ClientSecretHash: secretHash}, nil
}

// ListClients returns every non-revoked client for a tenant's own admin UI — secret hash is
// never selected, so there is no ClientWithSecret-shaped row this function could even
// accidentally leak.
func ListClients(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]*OAuthClient, error) {
	rows, err := db.Query(ctx,
		`SELECT `+clientColumns+`
		 FROM oauth_clients WHERE tenant_id = $1 AND revoked_at IS NULL ORDER BY created_at`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*OAuthClient
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeClient marks a client revoked — scoped to tenantID so an admin can never revoke another
// tenant's client by guessing its internal id. Deliberately doesn't cascade-revoke that client's
// live refresh tokens: a revoked client can no longer complete the token endpoint at all (client
// auth itself fails first, before any refresh-token lookup), which already closes off further
// access — see ConsumeRefreshToken's doc comment for where that check lives.
func RevokeClient(ctx context.Context, db DBTX, tenantID, id uuid.UUID) (bool, error) {
	tag, err := db.Exec(ctx,
		`UPDATE oauth_clients SET revoked_at = now() WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL`,
		id, tenantID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// VerifyClientSecret reports whether rawSecret hashes to this client's stored secret and the
// client isn't revoked — the one check the token endpoint's client authentication needs, kept
// here next to the hash it compares against rather than duplicated at the HTTP layer.
func VerifyClientSecret(client *ClientWithSecret, rawSecret string) bool {
	return client.Client.RevokedAt == nil && HashToken(rawSecret) == client.ClientSecretHash
}
