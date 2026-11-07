// TenantRegistry is an interface, not a concrete type, for the same reason PolicyStore is —
// swapping storage without touching Router/RegistryCache.
//
// Divergence from the Rust original: every tenant id here is a plain uuid.UUID, not a TenantId
// newtype wrapper — every other package in this Go port already passes tenant ids as bare
// uuid.UUID (permission.RequestContext, workflow, etc.), so introducing a wrapper only in this
// package would be inconsistent rather than safer.
package control

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TenantRegistry interface {
	Get(ctx context.Context, tenantID uuid.UUID) (*TenantRouting, error)
}

// TenantSummary is one control.tenants row, for listing — unlike TenantRouting (which Router
// needs and nothing else), this carries the columns an operator actually wants to see.
type TenantSummary struct {
	ID             uuid.UUID
	Tier           string
	Strategy       TenantStrategy
	Status         TenantStatus
	CreatedAt      time.Time
	TrialExpiresAt *time.Time
	Product        *string
}

func parseStrategy(strategyCol string, schemaName, dsnSecretRef *string) (TenantStrategy, error) {
	switch strategyCol {
	case "schema":
		if schemaName == nil {
			return TenantStrategy{}, fmt.Errorf("control.tenants row with strategy='schema' has no schema_name")
		}
		return SchemaStrategy(*schemaName), nil
	case "dedicated_db":
		if dsnSecretRef == nil {
			return TenantStrategy{}, fmt.Errorf("control.tenants row with strategy='dedicated_db' has no dsn_secret_ref")
		}
		return DedicatedDbStrategy(*dsnSecretRef), nil
	default:
		return TenantStrategy{}, fmt.Errorf("unknown control.tenants.strategy value: %s", strategyCol)
	}
}

type PostgresTenantRegistry struct {
	pool *pgxpool.Pool
}

func NewPostgresTenantRegistry(pool *pgxpool.Pool) *PostgresTenantRegistry {
	return &PostgresTenantRegistry{pool: pool}
}

// Provision writes a new control.tenants row — the provisioning-side counterpart to Get.
// schemaName/dsnSecretRef are mutually exclusive depending on strategy, same shape TenantStrategy
// enforces on the read side.
func (r *PostgresTenantRegistry) Provision(ctx context.Context, id uuid.UUID, tier, strategy string, schemaName, dsnSecretRef *string, status string) error {
	_, err := r.pool.Exec(ctx,
		"INSERT INTO control.tenants (id, tier, strategy, schema_name, dsn_secret_ref, status) VALUES ($1, $2, $3, $4, $5, $6)",
		id, tier, strategy, schemaName, dsnSecretRef, status)
	return err
}

// SetStatus flips status on an existing tenant. Returns (false, nil) when id doesn't match any
// row, so the caller can turn that into a clean 404.
func (r *PostgresTenantRegistry) SetStatus(ctx context.Context, id uuid.UUID, status string) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE control.tenants SET status = $2 WHERE id = $1", id, status)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetProduct sets which downstream product/app a tenant belongs to.
func (r *PostgresTenantRegistry) SetProduct(ctx context.Context, id uuid.UUID, product string) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE control.tenants SET product = $2 WHERE id = $1", id, product)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// GetProduct reads a tenant's product — nil both when the tenant has no row at all and when it
// has one but never had a product set.
func (r *PostgresTenantRegistry) GetProduct(ctx context.Context, id uuid.UUID) (*string, error) {
	var product *string
	err := r.pool.QueryRow(ctx, "SELECT product FROM control.tenants WHERE id = $1", id).Scan(&product)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return product, nil
}

// Deprovision sets status = 'deleted'. Idempotent: deprovisioning an already-deleted tenant is a
// no-op, still returns true. Returns (false, nil) when id doesn't match any row.
func (r *PostgresTenantRegistry) Deprovision(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE control.tenants SET status = 'deleted' WHERE id = $1", id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// List returns every control.tenants row, newest first. A row whose strategy/status value
// doesn't parse is skipped with a warning rather than failing the whole listing.
func (r *PostgresTenantRegistry) List(ctx context.Context) ([]TenantSummary, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT id, tier, strategy, schema_name, dsn_secret_ref, status, created_at, trial_expires_at, product FROM control.tenants ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []TenantSummary
	for rows.Next() {
		var id uuid.UUID
		var tier, strategyCol, statusCol string
		var schemaName, dsnSecretRef, product *string
		var createdAt time.Time
		var trialExpiresAt *time.Time
		if err := rows.Scan(&id, &tier, &strategyCol, &schemaName, &dsnSecretRef, &statusCol, &createdAt, &trialExpiresAt, &product); err != nil {
			return nil, err
		}

		strategy, strategyErr := parseStrategy(strategyCol, schemaName, dsnSecretRef)
		status, statusErr := ParseTenantStatus(statusCol)
		if strategyErr != nil || statusErr != nil {
			continue
		}

		summaries = append(summaries, TenantSummary{
			ID: id, Tier: tier, Strategy: strategy, Status: status,
			CreatedAt: createdAt, TrialExpiresAt: trialExpiresAt, Product: product,
		})
	}
	return summaries, rows.Err()
}

func (r *PostgresTenantRegistry) Get(ctx context.Context, tenantID uuid.UUID) (*TenantRouting, error) {
	var strategyCol, statusCol string
	var schemaName, dsnSecretRef *string
	err := r.pool.QueryRow(ctx,
		"SELECT strategy, schema_name, dsn_secret_ref, status FROM control.tenants WHERE id = $1", tenantID).
		Scan(&strategyCol, &schemaName, &dsnSecretRef, &statusCol)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	strategy, err := parseStrategy(strategyCol, schemaName, dsnSecretRef)
	if err != nil {
		return nil, err
	}
	status, err := ParseTenantStatus(statusCol)
	if err != nil {
		return nil, err
	}
	return &TenantRouting{Status: status, Strategy: strategy}, nil
}
