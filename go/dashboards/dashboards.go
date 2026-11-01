// Package dashboards holds customizable dashboard layouts — ported from metap's Rust
// metap-dashboards crate. A plain ops-config table (dashboard_configs), no HTTP, no
// business-entity knowledge.
//
// Two levels, resolved by GetEffectiveDashboard: a tenant default (ownerUserID == uuid.Nil, one
// per tenant, meant to be admin-write-only — that check belongs at the HTTP layer, this package
// has no concept of roles) and a personal override (ownerUserID == <user>, any authenticated user
// writes only their own). A user with no personal layout falls back to the tenant default; a
// tenant with neither has nothing here at all.
package dashboards

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DashboardConfig mirrors metap-dashboards::DashboardConfig. OwnerUserID is uuid.Nil for the
// tenant default row — ownerUserID IS NULL in SQL — never a real zero-value user id in practice.
type DashboardConfig struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	OwnerUserID uuid.UUID
	HasOwner    bool
	Layout      json.RawMessage
	UpdatedAt   time.Time
	UpdatedBy   uuid.UUID
	HasUpdater  bool
}

const columns = "id, tenant_id, owner_user_id, layout, updated_at, updated_by"

type executor interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func rowToConfig(row pgx.Row) (*DashboardConfig, error) {
	var c DashboardConfig
	var ownerUserID, updatedBy *uuid.UUID
	if err := row.Scan(&c.ID, &c.TenantID, &ownerUserID, &c.Layout, &c.UpdatedAt, &updatedBy); err != nil {
		return nil, err
	}
	if ownerUserID != nil {
		c.OwnerUserID, c.HasOwner = *ownerUserID, true
	}
	if updatedBy != nil {
		c.UpdatedBy, c.HasUpdater = *updatedBy, true
	}
	return &c, nil
}

// GetPersonal returns a user's own saved layout, if any.
func GetPersonal(ctx context.Context, e executor, tenantID, userID uuid.UUID) (*DashboardConfig, error) {
	row := e.QueryRow(ctx,
		"SELECT "+columns+" FROM metadata.dashboard_configs WHERE tenant_id = $1 AND owner_user_id = $2",
		tenantID, userID)
	cfg, err := rowToConfig(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return cfg, err
}

// GetTenantDefault returns the tenant-wide default layout, if any.
func GetTenantDefault(ctx context.Context, e executor, tenantID uuid.UUID) (*DashboardConfig, error) {
	row := e.QueryRow(ctx,
		"SELECT "+columns+" FROM metadata.dashboard_configs WHERE tenant_id = $1 AND owner_user_id IS NULL",
		tenantID)
	cfg, err := rowToConfig(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return cfg, err
}

// GetEffectiveDashboard returns the caller's own personal layout if saved, else the tenant's
// shared default, else nil (nobody has configured anything yet).
func GetEffectiveDashboard(ctx context.Context, e executor, tenantID, userID uuid.UUID) (*DashboardConfig, error) {
	personal, err := GetPersonal(ctx, e, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if personal != nil {
		return personal, nil
	}
	return GetTenantDefault(ctx, e, tenantID)
}

func upsert(ctx context.Context, e executor, tenantID uuid.UUID, ownerUserID *uuid.UUID, layout json.RawMessage, updatedBy uuid.UUID) (*DashboardConfig, error) {
	row := e.QueryRow(ctx,
		`INSERT INTO metadata.dashboard_configs (tenant_id, owner_user_id, layout, updated_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, owner_user_id) DO UPDATE
		 SET layout = EXCLUDED.layout, updated_at = now(), updated_by = EXCLUDED.updated_by
		 RETURNING `+columns,
		tenantID, ownerUserID, layout, updatedBy)
	return rowToConfig(row)
}

// UpsertPersonal writes a user's own layout.
func UpsertPersonal(ctx context.Context, e executor, tenantID, userID uuid.UUID, layout json.RawMessage) (*DashboardConfig, error) {
	return upsert(ctx, e, tenantID, &userID, layout, userID)
}

// UpsertTenantDefault writes the tenant-wide default. Caller (the HTTP layer) must have already
// checked the actor is allowed to edit it — this function has no notion of roles.
func UpsertTenantDefault(ctx context.Context, e executor, tenantID uuid.UUID, layout json.RawMessage, updatedBy uuid.UUID) (*DashboardConfig, error) {
	return upsert(ctx, e, tenantID, nil, layout, updatedBy)
}
