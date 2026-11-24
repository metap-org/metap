package rolestore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The built-in roles: the tenant admin (every tenant) and the platform admin (platform tenant).
// Their rows always exist (EnsureBuiltinRoles) and can't be deleted.
const (
	BuiltinRoleAdmin         = "admin"
	BuiltinRolePlatformAdmin = "platform_admin"
)

// ErrRoleNotFound is returned for a role name the tenant has no row for.
var ErrRoleNotFound = errors.New("role not found")

// ErrRoleExists is returned when creating a role whose name the tenant already has.
var ErrRoleExists = errors.New("role already exists")

// Role is one row of a tenant's role catalog (metadata.roles, roadmap 114).
type Role struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Label       string
	Description string
	Builtin     bool
	CreatedAt   time.Time
	CreatedBy   *uuid.UUID
	UpdatedAt   time.Time
}

const roleColumns = `id, tenant_id, name, coalesce(label, ''), coalesce(description, ''), builtin,
	created_at, created_by, updated_at`

func scanRole(row pgx.Row) (Role, error) {
	var r Role
	err := row.Scan(&r.ID, &r.TenantID, &r.Name, &r.Label, &r.Description, &r.Builtin,
		&r.CreatedAt, &r.CreatedBy, &r.UpdatedAt)
	return r, err
}

// IsBuiltinRole reports whether name is a built-in role of tenant (platform_admin only exists in
// the platform tenant, the nil uuid).
func IsBuiltinRole(tenant uuid.UUID, name string) bool {
	return name == BuiltinRoleAdmin || (tenant == uuid.Nil && name == BuiltinRolePlatformAdmin)
}

// EnsureRoles gives every named role a catalog row if it has none yet. Writers that name a role
// outside the admin API (policies, role assignment, bootstrap) call it so the catalog always holds
// every role in use; nothing it adds is overwritten later.
func EnsureRoles(ctx context.Context, db DBTX, tenant uuid.UUID, names []string, createdBy *uuid.UUID) error {
	clean := make([]string, 0, len(names))
	builtin := make([]bool, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			clean = append(clean, n)
			builtin = append(builtin, IsBuiltinRole(tenant, n))
		}
	}
	if len(clean) == 0 {
		return nil
	}
	_, err := db.Exec(ctx, `INSERT INTO roles (tenant_id, name, builtin, created_by)
		SELECT $1, n, b, $4 FROM unnest($2::text[], $3::bool[]) AS t(n, b)
		ON CONFLICT (tenant_id, name) DO NOTHING`, tenant, clean, builtin, createdBy)
	return err
}

// EnsureBuiltinRoles makes sure tenant's built-in roles have their rows (a tenant provisioned
// after migration 0041 gets them on first use).
func EnsureBuiltinRoles(ctx context.Context, db DBTX, tenant uuid.UUID) error {
	names := []string{BuiltinRoleAdmin}
	if tenant == uuid.Nil {
		names = append(names, BuiltinRolePlatformAdmin)
	}
	return EnsureRoles(ctx, db, tenant, names, nil)
}

// ListRoleRows returns tenant's role catalog ordered by name; search matches name, label or
// description case-insensitively.
func ListRoleRows(ctx context.Context, db DBTX, tenant uuid.UUID, search string) ([]Role, error) {
	q := `SELECT ` + roleColumns + ` FROM roles WHERE tenant_id = $1`
	args := []any{tenant}
	if s := strings.TrimSpace(search); s != "" {
		q += ` AND (name ILIKE $2 OR label ILIKE $2 OR description ILIKE $2)`
		args = append(args, "%"+escapeLike(s)+"%")
	}
	rows, err := db.Query(ctx, q+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Role, 0)
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoleNames returns every role name in tenant's catalog.
func RoleNames(ctx context.Context, db DBTX, tenant uuid.UUID) ([]string, error) {
	rows, err := ListRoleRows(ctx, db, tenant, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out, nil
}

func GetRoleByName(ctx context.Context, db DBTX, tenant uuid.UUID, name string) (Role, error) {
	r, err := scanRole(db.QueryRow(ctx, `SELECT `+roleColumns+` FROM roles WHERE tenant_id = $1 AND name = $2`, tenant, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrRoleNotFound
	}
	return r, err
}

// CreateRole adds a role row; ErrRoleExists when the name is taken.
func CreateRole(ctx context.Context, db DBTX, tenant uuid.UUID, name, label, description string, createdBy *uuid.UUID) (Role, error) {
	r, err := scanRole(db.QueryRow(ctx, `INSERT INTO roles (tenant_id, name, label, description, builtin, created_by)
		VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5, $6)
		ON CONFLICT (tenant_id, name) DO NOTHING
		RETURNING `+roleColumns, tenant, name, label, description, IsBuiltinRole(tenant, name), createdBy))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrRoleExists
	}
	return r, err
}

// UpdateRole sets a role's label and description.
func UpdateRole(ctx context.Context, db DBTX, tenant uuid.UUID, name, label, description string) (Role, error) {
	r, err := scanRole(db.QueryRow(ctx, `UPDATE roles SET label = nullif($3, ''), description = nullif($4, ''), updated_at = now()
		WHERE tenant_id = $1 AND name = $2 RETURNING `+roleColumns, tenant, name, label, description))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrRoleNotFound
	}
	return r, err
}

// DeleteRole removes a role row. Callers check RoleUsage first: this does not touch user_roles or
// policies.
func DeleteRole(ctx context.Context, db DBTX, tenant uuid.UUID, name string) error {
	tag, err := db.Exec(ctx, `DELETE FROM roles WHERE tenant_id = $1 AND name = $2`, tenant, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRoleNotFound
	}
	return nil
}

// PolicyRef names one policy that mentions a role.
type PolicyRef struct {
	ID     uuid.UUID
	Entity string
	Action string
}

// PoliciesNamingRole returns every policy of tenant whose roles include name, basic and advanced.
func PoliciesNamingRole(ctx context.Context, db DBTX, tenant uuid.UUID, name string) ([]PolicyRef, error) {
	rows, err := db.Query(ctx, `SELECT id, entity, action FROM policies
		WHERE tenant_id = $1 AND jsonb_typeof(roles) = 'array' AND roles ? $2
		ORDER BY entity, action`, tenant, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PolicyRef, 0)
	for rows.Next() {
		var p PolicyRef
		if err := rows.Scan(&p.ID, &p.Entity, &p.Action); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PolicyCountsByRole counts, per role name, the policies of tenant that name it.
func PolicyCountsByRole(ctx context.Context, db DBTX, tenant uuid.UUID) (map[string]int, error) {
	rows, err := db.Query(ctx, `SELECT r, count(*) FROM policies, jsonb_array_elements_text(roles) AS r
		WHERE tenant_id = $1 AND jsonb_typeof(roles) = 'array' GROUP BY r`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// escapeLike escapes LIKE's wildcards in a user-typed search.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// RoleGrant is one basic permission: the role may do action on entity. action is a policy action
// (read, create, update, delete, transition) or `transition:<workflow action>`.
type RoleGrant struct {
	Entity string
	Action string
}

// basicShape is the "role may do action" row the permission matrix and SetRolePermissions write;
// the same predicate SyncBasicPolicies and platform-ui's isBasicShapedRow use.
const basicShape = `condition IS NULL AND field IS NULL AND subject = 'context' AND effect = 'allow'`

// RoleBasicGrants returns role's basic grants in tenant, ordered by entity then action.
func RoleBasicGrants(ctx context.Context, db DBTX, tenant uuid.UUID, role string) ([]RoleGrant, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT entity, action FROM policies
		WHERE tenant_id = $1 AND jsonb_typeof(roles) = 'array' AND roles ? $2 AND `+basicShape+`
		ORDER BY entity, action`, tenant, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RoleGrant, 0)
	for rows.Next() {
		var g RoleGrant
		if err := rows.Scan(&g.Entity, &g.Action); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceRoleBasicGrants makes grants exactly role's basic permissions in tenant, leaving every
// other row alone (other roles', and every advanced row: conditional, field-level, record-subject,
// deny). A basic row shared with other roles loses only this role. Returns the entities whose
// policies changed. Run it in a transaction.
func ReplaceRoleBasicGrants(ctx context.Context, db DBTX, tenant uuid.UUID, role string, grants []RoleGrant, createdBy *uuid.UUID) ([]string, error) {
	touched := map[string]bool{}
	collect := func(sql string, args ...any) error {
		rows, err := db.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			touched[e] = true
		}
		return rows.Err()
	}
	if err := collect(`UPDATE policies SET roles = roles - $2::text
		WHERE tenant_id = $1 AND jsonb_typeof(roles) = 'array' AND jsonb_array_length(roles) > 1
		  AND roles ? $2 AND `+basicShape+` RETURNING entity`, tenant, role); err != nil {
		return nil, err
	}
	if err := collect(`DELETE FROM policies
		WHERE tenant_id = $1 AND roles = jsonb_build_array($2::text) AND `+basicShape+` RETURNING entity`,
		tenant, role); err != nil {
		return nil, err
	}
	seen := map[RoleGrant]bool{}
	for _, g := range grants {
		if seen[g] {
			continue
		}
		seen[g] = true
		if _, err := db.Exec(ctx, `INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect)
			VALUES ($1, $2, $3, jsonb_build_array($4::text), NULL, $5, NULL, 'context', 'allow')`,
			tenant, g.Entity, g.Action, role, createdBy); err != nil {
			return nil, err
		}
		touched[g.Entity] = true
	}
	out := make([]string, 0, len(touched))
	for e := range touched {
		out = append(out, e)
	}
	notify(Change{Tenant: tenant, Policies: true})
	return out, nil
}

// AdvancedPolicy is a policy naming a role that isn't a plain grant: it has a condition, targets a
// field or the record subject, or denies.
type AdvancedPolicy struct {
	ID        uuid.UUID
	Entity    string
	Action    string
	Roles     []string
	Effect    string
	Field     string
	Subject   string
	Condition []byte // raw JSON, nil when absent
}

// AdvancedPoliciesNamingRole returns role's advanced policies in tenant.
func AdvancedPoliciesNamingRole(ctx context.Context, db DBTX, tenant uuid.UUID, role string) ([]AdvancedPolicy, error) {
	rows, err := db.Query(ctx, `SELECT id, entity, action, roles, effect, coalesce(field, ''), subject, condition
		FROM policies
		WHERE tenant_id = $1 AND jsonb_typeof(roles) = 'array' AND roles ? $2 AND NOT (`+basicShape+`)
		ORDER BY entity, action`, tenant, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AdvancedPolicy, 0)
	for rows.Next() {
		var p AdvancedPolicy
		var rolesJSON []byte
		if err := rows.Scan(&p.ID, &p.Entity, &p.Action, &rolesJSON, &p.Effect, &p.Field, &p.Subject, &p.Condition); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(rolesJSON, &p.Roles)
		out = append(out, p)
	}
	return out, rows.Err()
}
