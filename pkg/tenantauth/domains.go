package tenantauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// VerifyRecordPrefix is the label a domain's ownership record lives under:
// `_metap-verify.<domain>` with the domain's token as a TXT value.
const VerifyRecordPrefix = "_metap-verify."

// ErrDomainClaimed: another tenant already proved it owns the domain.
var ErrDomainClaimed = errors.New("another workspace has already verified this domain")

// SSODomain is one domain a tenant named, and whether it has proved it owns it.
type SSODomain struct {
	Domain     string
	Token      string
	VerifiedAt *time.Time
}

// VerifyHost is the DNS name to publish the token under.
func (d SSODomain) VerifyHost() string { return VerifyRecordPrefix + d.Domain }

// SyncDomains makes the tenant's domain rows match domains: new ones get a pending row with a fresh
// token, ones no longer named are dropped, and the rest (verified or not) are left alone.
func SyncDomains(ctx context.Context, db DBTX, tenant uuid.UUID, domains []string) error {
	keep := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		keep = append(keep, d)
		if _, err := db.Exec(ctx, `INSERT INTO sso_domains (tenant_id, domain, token) VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, domain) DO NOTHING`, tenant, d, generateRandomToken()); err != nil {
			return err
		}
	}
	_, err := db.Exec(ctx, `DELETE FROM sso_domains WHERE tenant_id = $1 AND NOT (domain = ANY($2))`, tenant, keep)
	return err
}

// Domains lists the tenant's domains, oldest first.
func Domains(ctx context.Context, db DBTX, tenant uuid.UUID) ([]SSODomain, error) {
	rows, err := db.Query(ctx, `SELECT domain, token, verified_at FROM sso_domains WHERE tenant_id = $1 ORDER BY created_at, domain`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSODomain{}
	for rows.Next() {
		var d SSODomain
		if err := rows.Scan(&d.Domain, &d.Token, &d.VerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDomainVerified records that the tenant proved the domain. ErrDomainClaimed when another
// tenant got there first; a tenant that never named the domain gets (false, nil).
func MarkDomainVerified(ctx context.Context, db DBTX, tenant uuid.UUID, domain string, now time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE sso_domains SET verified_at = $3 WHERE tenant_id = $1 AND domain = $2 AND verified_at IS NULL`,
		tenant, strings.ToLower(domain), now)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return false, ErrDomainClaimed
	}
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		var one int
		err := db.QueryRow(ctx, `SELECT 1 FROM sso_domains WHERE tenant_id = $1 AND domain = $2`, tenant, strings.ToLower(domain)).Scan(&one)
		return err == nil, nil
	}
	return true, nil
}

// DomainVerifiedFor reports whether the tenant has proved it owns the domain of address.
func DomainVerifiedFor(ctx context.Context, db DBTX, tenant uuid.UUID, address string) (bool, error) {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false, nil
	}
	var verified bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sso_domains
		WHERE tenant_id = $1 AND domain = $2 AND verified_at IS NOT NULL)`, tenant, strings.ToLower(address[at+1:])).Scan(&verified)
	return verified, err
}

// TXTLookup resolves a TXT record (net.Resolver.LookupTXT in production, a fake in tests).
type TXTLookup func(ctx context.Context, name string) ([]string, error)

// CheckDomain looks up the ownership record and, when the token is there, marks the domain
// verified. (false, nil) means the record is not there (yet).
func CheckDomain(ctx context.Context, db DBTX, lookup TXTLookup, tenant uuid.UUID, d SSODomain, now time.Time) (bool, error) {
	if d.VerifiedAt != nil {
		return true, nil
	}
	values, err := lookup(ctx, d.VerifyHost())
	if err != nil {
		return false, nil // not found or the lookup failed: not verified, try again later
	}
	for _, v := range values {
		if strings.TrimSpace(v) == d.Token {
			return MarkDomainVerified(ctx, db, tenant, d.Domain, now)
		}
	}
	return false, nil
}
