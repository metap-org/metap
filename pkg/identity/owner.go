package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OwnerRecoveryWait is how long a recovery waits before it can complete, during which the
// tenant's admins can cancel it (roadmap 115 M8).
const OwnerRecoveryWait = 72 * time.Hour

var (
	ErrRecoveryPending  = errors.New("an owner recovery is already pending for this tenant")
	ErrRecoveryNotReady = errors.New("the owner recovery waiting period has not ended")
	ErrNoRecovery       = errors.New("no pending owner recovery")
)

// OwnerID is the tenant's owner, or uuid.Nil when none is assigned yet.
func OwnerID(ctx context.Context, db DBTX, tenantID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx, `SELECT id FROM metadata.users WHERE tenant_id = $1 AND is_owner AND deleted_at IS NULL`, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// SetOwner makes memberID the tenant's owner (clearing the previous one).
func SetOwner(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID) error {
	if _, err := db.Exec(ctx, `UPDATE metadata.users SET is_owner = false WHERE tenant_id = $1 AND is_owner`, tenantID); err != nil {
		return err
	}
	return expectOne(db.Exec(ctx, `UPDATE metadata.users SET is_owner = true, updated_at = now()
		WHERE id = $2 AND tenant_id = $1 AND deleted_at IS NULL`, tenantID, memberID))
}

// OwnerRecovery is a platform admin's request to hand a tenant's ownership to someone else.
type OwnerRecovery struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	NewOwnerMember   uuid.UUID
	RequestedBy      uuid.UUID
	RequestedByEmail string
	Reason           string
	EffectiveAt      time.Time
	CancelledAt      *time.Time
	CancelledBy      *uuid.UUID
	CompletedAt      *time.Time
	CreatedAt        time.Time
}

const ownerRecoveryColumns = `id, tenant_id, new_owner_member, requested_by, requested_by_email, reason, effective_at,
	cancelled_at, cancelled_by, completed_at, created_at`

func scanOwnerRecovery(row pgx.Row) (*OwnerRecovery, error) {
	var r OwnerRecovery
	err := row.Scan(&r.ID, &r.TenantID, &r.NewOwnerMember, &r.RequestedBy, &r.RequestedByEmail, &r.Reason, &r.EffectiveAt,
		&r.CancelledAt, &r.CancelledBy, &r.CompletedAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoRecovery
	}
	return &r, err
}

// RequestOwnerRecovery records a recovery that can complete after OwnerRecoveryWait.
func RequestOwnerRecovery(ctx context.Context, db DBTX, tenantID, newOwner, requestedBy uuid.UUID, email, reason string, now time.Time) (*OwnerRecovery, error) {
	if reason == "" || len(reason) > 500 {
		return nil, ErrSupportReason
	}
	r, err := scanOwnerRecovery(db.QueryRow(ctx, `INSERT INTO metadata.owner_recoveries
		(tenant_id, new_owner_member, requested_by, requested_by_email, reason, effective_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+ownerRecoveryColumns,
		tenantID, newOwner, requestedBy, email, reason, now.Add(OwnerRecoveryWait), now))
	if err != nil && isUniqueViolation(err) {
		return nil, ErrRecoveryPending
	}
	return r, err
}

// PendingOwnerRecovery is the tenant's recovery in flight, or ErrNoRecovery.
func PendingOwnerRecovery(ctx context.Context, db DBTX, tenantID uuid.UUID) (*OwnerRecovery, error) {
	return scanOwnerRecovery(db.QueryRow(ctx, `SELECT `+ownerRecoveryColumns+` FROM metadata.owner_recoveries
		WHERE tenant_id = $1 AND cancelled_at IS NULL AND completed_at IS NULL`, tenantID))
}

// CancelOwnerRecovery cancels the pending recovery (by a tenant admin).
func CancelOwnerRecovery(ctx context.Context, db DBTX, tenantID, by uuid.UUID, now time.Time) (*OwnerRecovery, error) {
	return scanOwnerRecovery(db.QueryRow(ctx, `UPDATE metadata.owner_recoveries SET cancelled_at = $3, cancelled_by = $2
		WHERE tenant_id = $1 AND cancelled_at IS NULL AND completed_at IS NULL RETURNING `+ownerRecoveryColumns, tenantID, by, now))
}

// CompleteOwnerRecovery marks the pending recovery done once its wait has passed; the caller then
// moves the ownership.
func CompleteOwnerRecovery(ctx context.Context, db DBTX, tenantID uuid.UUID, now time.Time) (*OwnerRecovery, error) {
	r, err := PendingOwnerRecovery(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}
	if now.Before(r.EffectiveAt) {
		return r, ErrRecoveryNotReady
	}
	return scanOwnerRecovery(db.QueryRow(ctx, `UPDATE metadata.owner_recoveries SET completed_at = $2
		WHERE id = $1 RETURNING `+ownerRecoveryColumns, r.ID, now))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
