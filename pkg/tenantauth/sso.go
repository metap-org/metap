package tenantauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SSOStateTTL is how long a started sign-in can be completed.
const SSOStateTTL = 10 * time.Minute

var ErrSSOState = errors.New("the sign-in link is unknown, used or expired")

// SSOState is what /auth/sso/start remembers for /auth/sso/callback.
type SSOState struct {
	Tenant       uuid.UUID
	Nonce        string
	PKCEVerifier string
}

// CreateSSOState stores one sign-in in flight and returns its state token.
func CreateSSOState(ctx context.Context, db DBTX, tenant uuid.UUID, state, nonce, verifier string, now time.Time) error {
	_, _ = db.Exec(ctx, `DELETE FROM metadata.sso_states WHERE expires_at < $1`, now)
	_, err := db.Exec(ctx, `INSERT INTO metadata.sso_states (state, tenant_id, nonce, pkce_verifier, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, state, tenant, nonce, verifier, now.Add(SSOStateTTL), now)
	return err
}

// ConsumeSSOState returns and deletes the sign-in's state; a second use, or one past its
// lifetime, is ErrSSOState.
func ConsumeSSOState(ctx context.Context, db DBTX, state string, now time.Time) (*SSOState, error) {
	var s SSOState
	err := db.QueryRow(ctx, `DELETE FROM metadata.sso_states WHERE state = $1 AND expires_at > $2
		RETURNING tenant_id, nonce, pkce_verifier`, state, now).Scan(&s.Tenant, &s.Nonce, &s.PKCEVerifier)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOState
	}
	return &s, err
}

// SaveOidcConfig stores the tenant's OIDC settings, replacing any previous ones.
func SaveOidcConfig(ctx context.Context, db DBTX, tenant uuid.UUID, cfg *OidcConfig, enabled bool) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config)
		VALUES ($1, 'oidc', $2, $3)
		ON CONFLICT (tenant_id, provider_kind) DO UPDATE SET enabled = $2, config = $3, updated_at = now()`, tenant, enabled, raw)
	return err
}

// StoredOidcConfig returns the tenant's OIDC settings whether or not they are enabled, with the
// enabled flag; (nil, false, nil) when it has none.
func StoredOidcConfig(ctx context.Context, db DBTX, tenant uuid.UUID) (*OidcConfig, bool, error) {
	var raw []byte
	var enabled bool
	err := db.QueryRow(ctx, `SELECT config, enabled FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oidc'`,
		tenant).Scan(&raw, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var cfg OidcConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false, err
	}
	return &cfg, enabled, nil
}

// DeleteOidcConfig removes the tenant's OIDC settings.
func DeleteOidcConfig(ctx context.Context, db DBTX, tenant uuid.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oidc'`, tenant)
	return err
}

// SSOEnforced reports whether the tenant has turned password sign-in off, through either provider.
func SSOEnforced(ctx context.Context, db DBTX, tenant uuid.UUID) (bool, error) {
	oidc, err := OidcConfigFor(ctx, db, tenant)
	if err != nil {
		return false, err
	}
	if oidc != nil && oidc.Enforced {
		return true, nil
	}
	saml, err := SamlConfigFor(ctx, db, tenant)
	if err != nil {
		return false, err
	}
	return saml != nil && saml.Enforced, nil
}

// ProviderOf names the single sign-on protocol the tenant has switched on: "oidc", "saml" or "".
func ProviderOf(ctx context.Context, db DBTX, tenant uuid.UUID) (string, error) {
	if cfg, err := OidcConfigFor(ctx, db, tenant); err != nil || cfg != nil {
		return "oidc", err
	}
	if cfg, err := SamlConfigFor(ctx, db, tenant); err != nil || cfg != nil {
		return ProviderSAML, err
	}
	return "", nil
}

// TenantsForEmailDomain lists the tenants whose enabled OIDC settings name domain and that have
// verified owning it.
func TenantsForEmailDomain(ctx context.Context, db DBTX, domain string) ([]uuid.UUID, error) {
	// Only a tenant that proved it owns the domain is routed to; naming it is not enough.
	rows, err := db.Query(ctx, `SELECT c.tenant_id FROM tenant_auth_configs c
		JOIN sso_domains d ON d.tenant_id = c.tenant_id AND d.domain = $1 AND d.verified_at IS NOT NULL
		WHERE c.provider_kind IN ('oidc', 'saml') AND c.enabled AND c.config->'emailDomains' ? $1`, strings.ToLower(domain))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EmailAllowed reports whether address is in the config's domain list (everything is allowed
// when the list is empty).
func (c *OidcConfig) EmailAllowed(address string) bool {
	return emailInDomains(c.EmailDomains, address)
}

func emailInDomains(domains []string, address string) bool {
	if len(domains) == 0 {
		return true
	}
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(address[at+1:])
	for _, d := range domains {
		if d == domain {
			return true
		}
	}
	return false
}
