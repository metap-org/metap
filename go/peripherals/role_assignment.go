// Package peripherals is a grab-bag of small platform-identity concerns ported from metap's
// Rust metap-peripherals crate: index reconciliation, metadata drift check, role assignment,
// local username/password auth + JWT minting, per-user preferences, and the AUTH_CONTEXT_ENTITY
// read side. No HTTP, no business-entity knowledge.
package peripherals

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx — most functions in this package work against
// either a bare pool or an already-open transaction (the Go equivalent of the Rust
// `impl sqlx::PgExecutor<'e>` genericity): tenant provisioning calls these against a bare pool it
// just opened (before a control.tenants row exists, so there's nothing to route through yet),
// while a caller with a resolved tenant calls them against an already-open, tenant-scoped
// transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// GetRolesForUser returns every role string assigned to a user in a tenant.
func GetRolesForUser(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) ([]string, error) {
	rows, err := db.Query(ctx, "SELECT role FROM user_roles WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// AssignRole grants a role; a duplicate assign is a no-op, not an error.
func AssignRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role string, assignedBy *uuid.UUID) error {
	_, err := db.Exec(ctx,
		`INSERT INTO user_roles (tenant_id, user_id, role, created_by) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
		tenantID, userID, role, assignedBy,
	)
	return err
}

// RevokeRole removes a role; a non-matching revoke is a silent no-op.
func RevokeRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role string) error {
	_, err := db.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2 AND role = $3",
		tenantID, userID, role)
	return err
}

type UserRoles struct {
	UserID uuid.UUID
	Roles  []string
}

// ListUsers returns every (user, [roles]) grouping for a tenant's admin UI.
func ListUsers(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]*UserRoles, error) {
	rows, err := db.Query(ctx, "SELECT user_id, role FROM user_roles WHERE tenant_id = $1", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// make(..., 0), not var grouped []*UserRoles — see ListTenantUsers's own comment on why
	// this matters for the wire shape of an empty result.
	grouped := make([]*UserRoles, 0)
	index := make(map[uuid.UUID]*UserRoles)
	for rows.Next() {
		var userID uuid.UUID
		var role string
		if err := rows.Scan(&userID, &role); err != nil {
			return nil, err
		}
		existing, ok := index[userID]
		if !ok {
			existing = &UserRoles{UserID: userID}
			index[userID] = existing
			grouped = append(grouped, existing)
		}
		existing.Roles = append(existing.Roles, role)
	}
	return grouped, rows.Err()
}
