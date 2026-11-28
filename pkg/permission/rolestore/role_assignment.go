// Package peripherals is a grab-bag of small platform-identity concerns. No HTTP, no
// business-entity knowledge.
package rolestore

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/pkg/permission"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx — most functions in this package work against
// either a bare pool or an already-open transaction.
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

// AssignRole grants a role; a duplicate assign is a no-op, not an error. A role the tenant's
// catalog doesn't have yet gets its row (EnsureRoles): validating the name is the admin API's job.
func AssignRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role string, assignedBy *uuid.UUID) error {
	if err := EnsureRoles(ctx, db, tenantID, []string{role}, assignedBy); err != nil {
		return err
	}
	_, err := db.Exec(ctx,
		`INSERT INTO user_roles (tenant_id, user_id, role, created_by) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
		tenantID, userID, role, assignedBy,
	)
	if err == nil {
		notify(Change{Tenant: tenantID, User: userID})
	}
	return err
}

// RevokeRole removes a role; a non-matching revoke is a silent no-op.
func RevokeRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role string) error {
	_, err := db.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2 AND role = $3",
		tenantID, userID, role)
	if err == nil {
		notify(Change{Tenant: tenantID, User: userID})
	}
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

// RolesForUsers returns each listed user's roles in tenantID (users without roles are absent).
func RolesForUsers(ctx context.Context, db DBTX, tenantID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	rows, err := db.Query(ctx, `SELECT user_id, role FROM user_roles
		WHERE tenant_id = $1 AND user_id = ANY($2) ORDER BY role`, tenantID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]string{}
	for rows.Next() {
		var id uuid.UUID
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out[id] = append(out[id], role)
	}
	return out, rows.Err()
}

// UsersWithRole returns the ids of every user holding role in tenantID.
func UsersWithRole(ctx context.Context, db DBTX, tenantID uuid.UUID, role string) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT user_id FROM user_roles WHERE tenant_id = $1 AND role = $2`, tenantID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RevokeAllRoles removes every role userID holds in tenantID (the member left the tenant).
func RevokeAllRoles(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) error {
	if _, err := db.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2", tenantID, userID); err != nil {
		return err
	}
	notify(Change{Tenant: tenantID, User: userID})
	return RevokeScopedRolesOf(ctx, db, tenantID, userID)
}

// ScopedRolesForUser returns the roles a user holds only inside a scope (roadmap 115 M1).
func ScopedRolesForUser(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) ([]permission.ScopedRole, error) {
	m, err := ScopedRolesForUsers(ctx, db, tenantID, []uuid.UUID{userID})
	return m[userID], err
}

// ScopedRolesForUsers is ScopedRolesForUser for several users at once.
func ScopedRolesForUsers(ctx context.Context, db DBTX, tenantID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID][]permission.ScopedRole, error) {
	out := map[uuid.UUID][]permission.ScopedRole{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT user_id, role, scope_type, scope_id FROM role_assignments
		WHERE tenant_id = $1 AND user_id = ANY($2) ORDER BY scope_type, scope_id, role`, tenantID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u uuid.UUID
		var sr permission.ScopedRole
		if err := rows.Scan(&u, &sr.Role, &sr.ScopeType, &sr.ScopeID); err != nil {
			return nil, err
		}
		out[u] = append(out[u], sr)
	}
	return out, rows.Err()
}

var scopeTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ErrInvalidScope: the scope type or id is not usable.
var ErrInvalidScope = errors.New("scope type must match ^[a-z][a-z0-9_]*$ and scope id must be 1 to 200 characters")

// ValidateScope checks a scope's shape.
func ValidateScope(scopeType, scopeID string) error {
	if !scopeTypePattern.MatchString(scopeType) || len(scopeType) > 80 || scopeID == "" || len(scopeID) > 200 {
		return ErrInvalidScope
	}
	return nil
}

// AssignScopedRole gives a role inside one scope; a duplicate is a no-op. Like AssignRole, a role
// the catalog lacks gets its row; validating the role name is the admin API's job.
func AssignScopedRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role, scopeType, scopeID string, assignedBy *uuid.UUID) error {
	if err := ValidateScope(scopeType, scopeID); err != nil {
		return err
	}
	if err := EnsureRoles(ctx, db, tenantID, []string{role}, assignedBy); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `INSERT INTO role_assignments (tenant_id, user_id, role, scope_type, scope_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, tenantID, userID, role, scopeType, scopeID, assignedBy)
	if err == nil {
		notify(Change{Tenant: tenantID, User: userID})
	}
	return err
}

// RevokeScopedRole removes a role from one scope; a non-matching revoke is a silent no-op.
func RevokeScopedRole(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, role, scopeType, scopeID string) error {
	_, err := db.Exec(ctx, `DELETE FROM role_assignments
		WHERE tenant_id = $1 AND user_id = $2 AND role = $3 AND scope_type = $4 AND scope_id = $5`,
		tenantID, userID, role, scopeType, scopeID)
	if err == nil {
		notify(Change{Tenant: tenantID, User: userID})
	}
	return err
}

// ScopeMember is one user's role on a scope.
type ScopeMember struct {
	UserID uuid.UUID
	Role   string
}

// MembersOfScope lists who holds which role on one scope ("who can see this zone").
func MembersOfScope(ctx context.Context, db DBTX, tenantID uuid.UUID, scopeType, scopeID string) ([]ScopeMember, error) {
	rows, err := db.Query(ctx, `SELECT user_id, role FROM role_assignments
		WHERE tenant_id = $1 AND scope_type = $2 AND scope_id = $3 ORDER BY role, user_id`, tenantID, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeMember
	for rows.Next() {
		var m ScopeMember
		if err := rows.Scan(&m.UserID, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RevokeScopedRolesOf removes every scoped role a user holds; used when they leave the tenant.
func RevokeScopedRolesOf(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) error {
	_, err := db.Exec(ctx, "DELETE FROM role_assignments WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
	if err == nil {
		notify(Change{Tenant: tenantID, User: userID})
	}
	return err
}

// RevokeScopedRolesOnScope removes every assignment on a scope; used when the scope record goes away.
func RevokeScopedRolesOnScope(ctx context.Context, db DBTX, tenantID uuid.UUID, scopeType, scopeID string) error {
	_, err := db.Exec(ctx, "DELETE FROM role_assignments WHERE tenant_id = $1 AND scope_type = $2 AND scope_id = $3",
		tenantID, scopeType, scopeID)
	if err == nil {
		notify(Change{Tenant: tenantID}) // a scope went away: any member's scoped roles may have changed
	}
	return err
}
