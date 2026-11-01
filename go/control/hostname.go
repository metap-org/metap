// Which tenant answers on a given hostname (control.tenant_hostnames). Exists for exactly one
// caller shape: a request that has no tenant yet — a public theme endpoint served before anyone
// has logged in, with a Host header as all it has to go on. A Host header is attacker-
// controlled, so a caller must treat the resolved tenant as a presentation hint and never as an
// authorization fact.
package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NormalizeHostname lowercases, drops a trailing dot and any :port suffix, and refuses anything
// that isn't plausibly a hostname. Returns ("", false) rather than a sanitized best-effort: a
// header this far from a hostname is not a typo to be repaired.
func NormalizeHostname(raw string) (string, bool) {
	// An IPv6 literal ([::1]:8080) is never a tenant hostname here — reject rather than try to
	// parse brackets, since nothing would ever match anyway.
	if strings.HasPrefix(raw, "[") {
		return "", false
	}
	host, _, _ := strings.Cut(raw, ":")
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || len(host) > 253 {
		return "", false
	}
	for _, c := range host {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') {
			return "", false
		}
	}
	return host, true
}

// TenantIDForHostname returns the tenant registered for hostname, or (uuid.Nil, false) if no
// tenant claims it. A caller must render that as "the fleet defaults", never as a 404 —
// answering differently for a registered and an unregistered hostname turns an unauthenticated
// endpoint into a tenant-existence oracle.
func TenantIDForHostname(ctx context.Context, db DBTX, hostname string) (uuid.UUID, bool, error) {
	host, ok := NormalizeHostname(hostname)
	if !ok {
		return uuid.Nil, false, nil
	}
	var tenantID uuid.UUID
	err := db.QueryRow(ctx, "SELECT tenant_id FROM control.tenant_hostnames WHERE hostname = $1", host).Scan(&tenantID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return tenantID, true, nil
}

// SetTenantHostname registers (or re-points) a hostname. Operator-only by construction — nothing
// in an HTTP surface calls this.
func SetTenantHostname(ctx context.Context, db DBTX, tenantID uuid.UUID, hostname string) (string, error) {
	host, ok := NormalizeHostname(hostname)
	if !ok {
		return "", fmt.Errorf("%q is not a usable hostname", hostname)
	}
	_, err := db.Exec(ctx,
		"INSERT INTO control.tenant_hostnames (hostname, tenant_id) VALUES ($1, $2) ON CONFLICT (hostname) DO UPDATE SET tenant_id = EXCLUDED.tenant_id",
		host, tenantID)
	if err != nil {
		return "", err
	}
	return host, nil
}
