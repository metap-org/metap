// The read side of AUTH_CONTEXT_ENTITY — looks up the caller's own record on a configured
// entity by a userId field, generic over entity shape. Not a CrudService.Get call — that would
// run the *target's own* permission check against the very context being built, which is
// circular; this is a raw, unauthenticated read of the caller's own identity data, same trust
// level as GetRolesForUser.
package peripherals

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FetchContextAttributes reads the caller's own record on a configured AUTH_CONTEXT_ENTITY.
// tableName (not an entity name) is trusted, unparameterized SQL interpolation — safe only
// because the caller resolves it from EntityDefinition.table_name after schema-name validation,
// never from unvalidated request input.
func FetchContextAttributes(ctx context.Context, db DBTX, tenantID uuid.UUID, tableName string, userID uuid.UUID) (map[string]any, error) {
	// Every field is its own column (Phase 106): the record is the row as JSON, minus the
	// framework bookkeeping columns, so the caller sees the entity's fields (plus code/status).
	row := db.QueryRow(ctx,
		"SELECT to_jsonb(t) - ARRAY['id','tenant_id','deleted','version','created_at','updated_at','created_by','updated_by'] "+
			"FROM "+tableName+" t WHERE t.tenant_id = $1 AND t.deleted = false AND t.\"userId\"::text = $2 LIMIT 1",
		tenantID, userID.String(),
	)
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}
