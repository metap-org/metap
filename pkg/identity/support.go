package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bounds of one support grant (roadmap 115 M10).
const (
	SupportDefaultHours = 24
	SupportMaxHours     = 168 // a week
	// SupportSessionMaxHours caps how long one started session lasts even under a longer grant, so
	// support has to come back (and be audited again) at least this often.
	SupportSessionMaxHours = 8
)

var (
	ErrSupportGrantInactive = errors.New("the support grant is revoked or expired")
	ErrSupportReason        = errors.New("a reason of 1-500 characters is required")
)

// SupportGrant is a tenant admin's permission for support staff to act in the tenant.
type SupportGrant struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	GrantedBy uuid.UUID
	Reason    string
	Role      string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
	Sessions  []SupportSession
}

// Active reports whether the grant can still be used at now.
func (g SupportGrant) Active(now time.Time) bool { return g.RevokedAt == nil && g.ExpiresAt.After(now) }

// SupportSession is one started use of a grant: the member and key that were created for it.
type SupportSession struct {
	ID             uuid.UUID
	GrantID        uuid.UUID
	MemberID       uuid.UUID
	KeyID          uuid.UUID
	StartedBy      uuid.UUID
	StartedByEmail string
	StartedAt      time.Time
}

const supportGrantColumns = `id, tenant_id, granted_by, reason, role, expires_at, revoked_at, created_at`

func scanSupportGrant(row pgx.Row) (*SupportGrant, error) {
	var g SupportGrant
	if err := row.Scan(&g.ID, &g.TenantID, &g.GrantedBy, &g.Reason, &g.Role, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

// CreateSupportGrant records the tenant's permission; hours is clamped to 1..SupportMaxHours
// (0 means the default).
func CreateSupportGrant(ctx context.Context, db DBTX, tenantID, grantedBy uuid.UUID, reason, role string, hours int, now time.Time) (*SupportGrant, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		return nil, ErrSupportReason
	}
	if hours <= 0 {
		hours = SupportDefaultHours
	}
	hours = min(hours, SupportMaxHours)
	return scanSupportGrant(db.QueryRow(ctx, `INSERT INTO metadata.support_grants
		(tenant_id, granted_by, reason, role, expires_at, created_at) VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+supportGrantColumns, tenantID, grantedBy, reason, role, now.Add(time.Duration(hours)*time.Hour), now))
}

// GetSupportGrant returns one grant of the tenant with its sessions.
func GetSupportGrant(ctx context.Context, db DBTX, tenantID, id uuid.UUID) (*SupportGrant, error) {
	g, err := scanSupportGrant(db.QueryRow(ctx, `SELECT `+supportGrantColumns+` FROM metadata.support_grants
		WHERE id = $1 AND tenant_id = $2`, id, tenantID))
	if err != nil {
		return nil, err
	}
	s, err := supportSessions(ctx, db, []uuid.UUID{g.ID})
	if err != nil {
		return nil, err
	}
	g.Sessions = s[g.ID]
	return g, nil
}

// LatestActiveSupportGrant is the tenant's newest grant that is neither revoked nor expired.
func LatestActiveSupportGrant(ctx context.Context, db DBTX, tenantID uuid.UUID, now time.Time) (*SupportGrant, error) {
	return scanSupportGrant(db.QueryRow(ctx, `SELECT `+supportGrantColumns+` FROM metadata.support_grants
		WHERE tenant_id = $1 AND revoked_at IS NULL AND expires_at > $2 ORDER BY created_at DESC LIMIT 1`, tenantID, now))
}

// ListSupportGrants returns the tenant's grants, newest first, each with its sessions.
func ListSupportGrants(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]SupportGrant, error) {
	rows, err := db.Query(ctx, `SELECT `+supportGrantColumns+` FROM metadata.support_grants
		WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 100`, tenantID)
	if err != nil {
		return nil, err
	}
	out := []SupportGrant{}
	var ids []uuid.UUID
	for rows.Next() {
		g, err := scanSupportGrant(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *g)
		ids = append(ids, g.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sessions, err := supportSessions(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Sessions = sessions[out[i].ID]
	}
	return out, nil
}

func supportSessions(ctx context.Context, db DBTX, grantIDs []uuid.UUID) (map[uuid.UUID][]SupportSession, error) {
	out := map[uuid.UUID][]SupportSession{}
	if len(grantIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT id, grant_id, member_id, key_id, started_by, started_by_email, started_at
		FROM metadata.support_sessions WHERE grant_id = ANY($1) ORDER BY started_at`, grantIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s SupportSession
		if err := rows.Scan(&s.ID, &s.GrantID, &s.MemberID, &s.KeyID, &s.StartedBy, &s.StartedByEmail, &s.StartedAt); err != nil {
			return nil, err
		}
		out[s.GrantID] = append(out[s.GrantID], s)
	}
	return out, rows.Err()
}

// RecordSupportSession links a started session to its grant.
func RecordSupportSession(ctx context.Context, db DBTX, s SupportSession, tenantID uuid.UUID) error {
	_, err := db.Exec(ctx, `INSERT INTO metadata.support_sessions
		(grant_id, tenant_id, member_id, key_id, started_by, started_by_email, started_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		s.GrantID, tenantID, s.MemberID, s.KeyID, s.StartedBy, s.StartedByEmail, s.StartedAt)
	return err
}

// RevokeSupportGrant ends a grant now: it is marked revoked, every key started under it is
// revoked and every session member removed, so support loses access at once. Idempotent. Returns
// the member ids removed (to clear their roles).
func RevokeSupportGrant(ctx context.Context, db DBTX, tenantID, id uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	g, err := GetSupportGrant(ctx, db, tenantID, id)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, `UPDATE metadata.support_grants SET revoked_at = coalesce(revoked_at, $3)
		WHERE id = $1 AND tenant_id = $2`, id, tenantID, now); err != nil {
		return nil, err
	}
	var members []uuid.UUID
	for _, s := range g.Sessions {
		if err := RevokeAPIKey(ctx, db, tenantID, s.KeyID, now); err != nil {
			return nil, err
		}
		if err := RemoveMember(ctx, db, tenantID, s.MemberID, now); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		members = append(members, s.MemberID)
	}
	return members, nil
}

// ExpireSupportSessions removes the session members of grants that have lapsed (their keys
// already stopped working), so the tenant's service-account list does not keep stale "Support"
// entries. Returns the member ids removed.
func ExpireSupportSessions(ctx context.Context, db DBTX, tenantID uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT s.member_id, s.key_id FROM metadata.support_sessions s
		JOIN metadata.support_grants g ON g.id = s.grant_id
		JOIN metadata.users u ON u.id = s.member_id AND u.deleted_at IS NULL
		WHERE s.tenant_id = $1 AND (g.revoked_at IS NOT NULL OR g.expires_at <= $2)`, tenantID, now)
	if err != nil {
		return nil, err
	}
	type pair struct{ member, key uuid.UUID }
	var stale []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.member, &p.key); err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var removed []uuid.UUID
	for _, p := range stale {
		if err := RevokeAPIKey(ctx, db, tenantID, p.key, now); err != nil {
			return nil, err
		}
		if err := RemoveMember(ctx, db, tenantID, p.member, now); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		removed = append(removed, p.member)
	}
	return removed, nil
}
