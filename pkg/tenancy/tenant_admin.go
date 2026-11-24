// Tenant administration on top of the registry: search, rename, and the delete-with-retention
// lifecycle. The state machine lives in SQL guards, not in callers, so two admins racing can't
// both win.
package tenancy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrTenantNotFound: no control.tenants row has that id.
	ErrTenantNotFound = errors.New("tenant not found")
	// ErrTenantState: the tenant is not in a state that allows the operation (a deleted tenant
	// can't be suspended, a deletion can't be requested twice, ...).
	ErrTenantState = errors.New("tenant is not in a state that allows this")
)

// DefaultDeletionRetention is how long a deletion request can be cancelled.
const DefaultDeletionRetention = 30 * 24 * time.Hour

// TenantDetail is a control.tenants row with the admin-facing columns.
type TenantDetail struct {
	TenantSummary
	Name                string
	DeletionRequestedAt *time.Time
	DeletionEffectiveAt *time.Time
}

// TenantFilter narrows Search. Zero values match everything.
type TenantFilter struct {
	// Query matches the tenant's name or id, case-insensitively.
	Query  string
	Status string
	Tier   string
	// Limit 0 means 50.
	Limit  int
	Offset int
}

const tenantDetailColumns = `id, coalesce(name, ''), tier, strategy, schema_name, dsn_secret_ref, status, created_at,
	trial_expires_at, product, deletion_requested_at, deletion_effective_at`

func scanTenantDetail(row pgx.Row) (*TenantDetail, error) {
	var d TenantDetail
	var strategyCol, statusCol string
	var schemaName, dsnSecretRef *string
	if err := row.Scan(&d.ID, &d.Name, &d.Tier, &strategyCol, &schemaName, &dsnSecretRef, &statusCol, &d.CreatedAt,
		&d.TrialExpiresAt, &d.Product, &d.DeletionRequestedAt, &d.DeletionEffectiveAt); err != nil {
		return nil, err
	}
	var err error
	if d.Strategy, err = parseStrategy(strategyCol, schemaName, dsnSecretRef); err != nil {
		return nil, err
	}
	if d.Status, err = ParseTenantStatus(statusCol); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDetail reads one tenant. ErrTenantNotFound when there is no such row.
func (r *PostgresTenantRegistry) GetDetail(ctx context.Context, id uuid.UUID) (*TenantDetail, error) {
	d, err := scanTenantDetail(r.db.QueryRow(ctx, "SELECT "+tenantDetailColumns+" FROM control.tenants WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	return d, err
}

// Search lists tenants newest first and the total matching the filter.
func (r *PostgresTenantRegistry) Search(ctx context.Context, f TenantFilter) ([]TenantDetail, int, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := `WHERE ($1 = '' OR name ILIKE '%' || $1 || '%' OR id::text = $1)
		AND ($2 = '' OR status = $2) AND ($3 = '' OR tier = $3)`
	q := strings.TrimSpace(f.Query)
	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM control.tenants "+where, escapeLike(q), f.Status, f.Tier).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(ctx, "SELECT "+tenantDetailColumns+" FROM control.tenants "+where+
		" ORDER BY created_at DESC, id LIMIT $4 OFFSET $5", escapeLike(q), f.Status, f.Tier, limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TenantDetail
	for rows.Next() {
		d, err := scanTenantDetail(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// RegisterShared registers a tenant whose data lives in the shared tables (tenant_id column, the
// default search_path), the layout products with very many small tenants use. The admin and
// roles are the caller's to create; the tenant is visible to Router from this insert on.
func (r *PostgresTenantRegistry) RegisterShared(ctx context.Context, id uuid.UUID, name, tier, status string) error {
	public := "public"
	_, err := r.db.Exec(ctx,
		`INSERT INTO control.tenants (id, name, tier, strategy, schema_name, status) VALUES ($1, $2, $3, 'schema', $4, $5)`,
		id, name, tier, public, status)
	return err
}

// Update renames a tenant and/or changes its tier; nil leaves a field alone.
func (r *PostgresTenantRegistry) Update(ctx context.Context, id uuid.UUID, name, tier *string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE control.tenants SET name = coalesce($2, name), tier = coalesce($3, tier) WHERE id = $1`, id, name, tier)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

func (r *PostgresTenantRegistry) transition(ctx context.Context, id uuid.UUID, sql string, args ...any) error {
	tag, err := r.db.Exec(ctx, sql, append([]any{id}, args...)...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	if _, err := r.GetDetail(ctx, id); err != nil {
		return err // ErrTenantNotFound
	}
	return ErrTenantState
}

// Suspend blocks an active tenant: Router refuses it until Resume.
func (r *PostgresTenantRegistry) Suspend(ctx context.Context, id uuid.UUID) error {
	return r.transition(ctx, id, `UPDATE control.tenants SET status = 'suspended'
		WHERE id = $1 AND status = 'active' AND deletion_requested_at IS NULL`)
}

// Resume reopens a suspended tenant. A tenant with a deletion pending is reopened by
// CancelDeletion, not here, so a stray Resume can't silently void a deletion request.
func (r *PostgresTenantRegistry) Resume(ctx context.Context, id uuid.UUID) error {
	return r.transition(ctx, id, `UPDATE control.tenants SET status = 'active'
		WHERE id = $1 AND status = 'suspended' AND deletion_requested_at IS NULL`)
}

// RequestDeletion suspends the tenant immediately and schedules it to be deleted after
// retention. Only an active or suspended tenant with no request already pending qualifies.
func (r *PostgresTenantRegistry) RequestDeletion(ctx context.Context, id uuid.UUID, now time.Time, retention time.Duration) error {
	if retention <= 0 {
		retention = DefaultDeletionRetention
	}
	return r.transition(ctx, id, `UPDATE control.tenants
		SET status = 'suspended', deletion_requested_at = $2, deletion_effective_at = $3
		WHERE id = $1 AND status IN ('active', 'suspended') AND deletion_requested_at IS NULL`,
		now, now.Add(retention))
}

// CancelDeletion reopens a tenant whose deletion has been requested and has not yet taken effect.
func (r *PostgresTenantRegistry) CancelDeletion(ctx context.Context, id uuid.UUID, now time.Time) error {
	return r.transition(ctx, id, `UPDATE control.tenants
		SET status = 'active', deletion_requested_at = NULL, deletion_effective_at = NULL
		WHERE id = $1 AND status = 'suspended' AND deletion_requested_at IS NOT NULL AND deletion_effective_at > $2`, now)
}

// PurgeDue marks every tenant whose retention has run out as deleted and returns their ids.
// Deleting is the registry state only (Router refuses the tenant for good); physical data
// removal is a separate, deliberate step.
func (r *PostgresTenantRegistry) PurgeDue(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `UPDATE control.tenants SET status = 'deleted'
		WHERE status <> 'deleted' AND deletion_effective_at IS NOT NULL AND deletion_effective_at <= $1
		RETURNING id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
