// The read side of AUTH_CONTEXT_ENTITY — looks up the caller's own record on a configured
// entity by a userId field, generic over entity shape. Not a CrudService.Get call — that would
// run the *target's own* permission check against the very context being built, which is
// circular; this is a raw, unauthenticated read of the caller's own identity data, same trust
// level as GetRolesForUser.
package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// FetchContextAttributes reads the caller's own record on a configured AUTH_CONTEXT_ENTITY; nil when
// there is none. More than one is ErrAmbiguousContext: picking one could leave out the record a
// policy denies on. tableName (not an entity name) is trusted, unparameterized SQL interpolation —
// safe only because the caller resolves it from EntityDefinition.table_name after schema-name
// validation, never from unvalidated request input.
func FetchContextAttributes(ctx context.Context, db DBTX, tenantID uuid.UUID, tableName string, userID uuid.UUID) (map[string]any, error) {
	// Every field is its own column (Phase 106): the record is the row as JSON, minus the
	// framework bookkeeping columns, so the caller sees the entity's fields (plus code/status).
	rows, err := db.Query(ctx,
		"SELECT to_jsonb(t) - ARRAY['id','tenant_id','deleted','version','created_at','updated_at','created_by','updated_by'] "+
			"FROM "+tableName+" t WHERE t.tenant_id = $1 AND t.deleted = false AND t.\"userId\"::text = $2 LIMIT 2",
		tenantID, userID.String(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		found = append(found, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		var data map[string]any
		if err := json.Unmarshal(found[0], &data); err != nil {
			return nil, err
		}
		return data, nil
	default:
		return nil, ErrAmbiguousContext
	}
}

// ErrAmbiguousContext: a member has more than one record of the AUTH_CONTEXT_ENTITY.
var ErrAmbiguousContext = errors.New("more than one record of the context entity names this member")
