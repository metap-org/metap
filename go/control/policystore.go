// Router-backed permission.PolicyStore implementation. Lives here rather than in the permission
// package purely to avoid a dependency cycle (same reasoning as the Rust original).
//
// Routes every query through Router.Begin(tenantID) instead of a fixed pool — a DedicatedDb-
// strategy tenant's policies table lives only in that tenant's own database.
//
// Divergence from the Rust original: that version runs `SELECT * FROM policies ...` and scans
// the row by column name (sqlx's try_get("id")/try_get("entity")/...). pgx has no equivalent
// generic "select star, scan by name" primitive as convenient as sqlx's, so this port uses an
// explicit column list (matching exactly the policies table's columns) and scans positionally
// instead — equivalent result, more robust than `SELECT *` either way.
package control

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/metap-org/metap-go/permission"
)

const policyColumns = `id, tenant_id, entity, action, field, subject, roles, condition, created_by, effect`

type PostgresPolicyStore struct {
	router *Router
}

func NewPostgresPolicyStore(router *Router) *PostgresPolicyStore {
	return &PostgresPolicyStore{router: router}
}

func scanPolicyRow(row pgx.Row) (permission.PolicyRow, error) {
	var p permission.PolicyRow
	var field *string
	var rolesRaw, conditionRaw []byte
	var effectCol string
	if err := row.Scan(&p.ID, &p.TenantID, &p.Entity, &p.Action, &field, &p.Subject, &rolesRaw, &conditionRaw, &p.CreatedBy, &effectCol); err != nil {
		return permission.PolicyRow{}, err
	}
	if field != nil {
		p.Field = *field
	}
	if rolesRaw != nil {
		if err := json.Unmarshal(rolesRaw, &p.Roles); err != nil {
			return permission.PolicyRow{}, err
		}
		p.HasRoles = true
	}
	if conditionRaw != nil {
		var cond permission.PolicyCondition
		if err := json.Unmarshal(conditionRaw, &cond); err != nil {
			return permission.PolicyRow{}, err
		}
		p.Condition = &cond
	}
	p.Effect = permission.ParsePolicyEffect(effectCol)
	return p, nil
}

func scanPolicyRows(rows pgx.Rows) ([]permission.PolicyRow, error) {
	defer rows.Close()
	var out []permission.PolicyRow
	for rows.Next() {
		p, err := scanPolicyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresPolicyStore) FindContextPolicies(ctx context.Context, tenantID uuid.UUID, entity, action string) ([]permission.PolicyRow, error) {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx,
		"SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND field IS NULL AND subject = 'context'",
		tenantID, entity, action)
	if err != nil {
		return nil, err
	}
	out, err := scanPolicyRows(rows)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *PostgresPolicyStore) LoadAllPolicies(ctx context.Context, tenantID uuid.UUID, entity string) ([]permission.PolicyRow, error) {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1 AND entity = $2", tenantID, entity)
	if err != nil {
		return nil, err
	}
	out, err := scanPolicyRows(rows)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *PostgresPolicyStore) FindExplainPolicies(ctx context.Context, tenantID uuid.UUID, entity, action string, opts permission.ExplainOptions) ([]permission.PolicyRow, error) {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var rows pgx.Rows
	if opts.Field != "" {
		rows, err = tx.Query(ctx,
			"SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND field = $4",
			tenantID, entity, action, opts.Field)
	} else {
		subject := permission.SubjectContext
		if opts.Subject != nil {
			subject = *opts.Subject
		}
		rows, err = tx.Query(ctx,
			"SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND field IS NULL AND subject = $4",
			tenantID, entity, action, subject.String())
	}
	if err != nil {
		return nil, err
	}
	out, err := scanPolicyRows(rows)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *PostgresPolicyStore) ListPolicies(ctx context.Context, tenantID uuid.UUID, entity string, hasEntity bool) ([]permission.PolicyRow, error) {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var rows pgx.Rows
	if hasEntity {
		rows, err = tx.Query(ctx, "SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1 AND entity = $2", tenantID, entity)
	} else {
		rows, err = tx.Query(ctx, "SELECT "+policyColumns+" FROM policies WHERE tenant_id = $1", tenantID)
	}
	if err != nil {
		return nil, err
	}
	out, err := scanPolicyRows(rows)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *PostgresPolicyStore) CreatePolicy(ctx context.Context, tenantID uuid.UUID, entity, action string, roles []string, hasRoles bool,
	condition *permission.PolicyCondition, createdBy *uuid.UUID, field string, subject *permission.PolicySubject, effect permission.PolicyEffect) (permission.PolicyRow, error) {
	subj := permission.SubjectContext
	if subject != nil {
		subj = *subject
	}
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return permission.PolicyRow{}, err
	}
	defer tx.Rollback(ctx)

	var rolesJSON, condJSON []byte
	if hasRoles {
		rolesJSON, _ = json.Marshal(roles)
	}
	if condition != nil {
		condJSON, _ = json.Marshal(condition)
	}
	var fieldArg *string
	if field != "" {
		fieldArg = &field
	}

	row := tx.QueryRow(ctx,
		"INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect) "+
			"VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING "+policyColumns,
		tenantID, entity, action, rolesJSON, condJSON, createdBy, fieldArg, subj.String(), effect.String())
	p, err := scanPolicyRow(row)
	if err != nil {
		return permission.PolicyRow{}, err
	}
	return p, tx.Commit(ctx)
}

func (s *PostgresPolicyStore) DeletePolicy(ctx context.Context, tenantID, id uuid.UUID) error {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "DELETE FROM policies WHERE tenant_id = $1 AND id = $2", tenantID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresPolicyStore) SyncBasicPolicies(ctx context.Context, tenantID uuid.UUID, entity string, grants []permission.RoleActionGrant, createdBy *uuid.UUID) ([]permission.PolicyRow, error) {
	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Delete every existing basic-shaped row for this entity — matches isBasicShapedRow on the
	// frontend exactly, so this can never touch an Advanced-tab row.
	if _, err := tx.Exec(ctx,
		"DELETE FROM policies WHERE tenant_id = $1 AND entity = $2 AND condition IS NULL AND field IS NULL AND subject = 'context' AND effect = 'allow'",
		tenantID, entity); err != nil {
		return nil, err
	}

	var out []permission.PolicyRow
	for _, g := range grants {
		var rolesJSON []byte
		if g.HasRole {
			rolesJSON, _ = json.Marshal([]string{g.Role})
		}
		row := tx.QueryRow(ctx,
			"INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect) "+
				"VALUES ($1, $2, $3, $4, NULL, $5, NULL, 'context', 'allow') RETURNING "+policyColumns,
			tenantID, entity, g.Action, rolesJSON, createdBy)
		p, err := scanPolicyRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, tx.Commit(ctx)
}
