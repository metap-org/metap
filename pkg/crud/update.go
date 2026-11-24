package crud

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

// Update changes a record: resolve, authorize the entity, load the record, authorize it, prepare
// the merged data, persist.
func (s *CrudService) Update(ctx context.Context, entityName string, id uuid.UUID, expectedVersion int32, rawData JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	c, err := s.resolve(ctx, rc, entityName)
	if err != nil {
		return RecordDto{}, err
	}
	if err := s.authorizeEntity(ctx, c, permission.ActionUpdate, ""); err != nil {
		return RecordDto{}, err
	}
	cur, err := s.loadCurrent(ctx, c, id, false)
	if err != nil {
		return RecordDto{}, err
	}
	snapshot, err := s.authorizeRecord(ctx, c, cur, permission.ActionUpdate, "")
	if err != nil {
		return RecordDto{}, err
	}
	p, err := prepareUpdate(c.entity, snapshot, rc, cur.record, rawData)
	if err != nil {
		return RecordDto{}, err
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return RecordDto{}, err
	}

	// System fields are not part of the write (see writableFields): they keep whatever is stored
	// now, so a concurrent SetSystemFields cannot be overwritten by this update.
	extraSet := "code = $6::varchar"
	if sv := plainStatusValue(c.entity, p.data); sv != nil {
		extraSet += ", status = " + quoteLit(*sv)
	}
	return s.persist(ctx, c, snapshot, write{
		userID: userID, reason: reason,
		sql:       buildUpdateSQL(c.entity, extraSet),
		args:      []any{json.RawMessage(p.dataJSON), userID, id, c.tenantID, expectedVersion, p.code},
		unique:    true,
		versioned: true,
		emit: func(ctx context.Context, tx pgx.Tx, record RecordDto) error {
			return workflow.EmitUpdated(ctx, tx, c.entity, c.tenantID, record.ID, p.data, record.Version)
		},
		after:       func() { s.invalidateContextCacheIfConfigured(c.entity.Name, c.tenantID, p.data) },
		auditAction: audit.ActionUpdate,
		diff: func(RecordDto) audit.JsonObject {
			return audit.DiffJSONObjects(audit.JsonObject(cur.record.Data), audit.JsonObject(p.data))
		},
	})
}

// updated is what prepareUpdate decides to write.
type updated struct {
	data     JsonObject
	dataJSON []byte
	code     *string
}

// prepareUpdate merges the caller's changes onto the stored record and checks the result. No I/O.
func prepareUpdate(entity metadata.EntityDefinition, snapshot *permission.PermissionSnapshot, rc *permission.RequestContext, existing *RecordDto, raw JsonObject) (updated, error) {
	if fe := rejectSystemFields(entity, raw); fe != nil {
		return updated{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fe)
	}
	if d := snapshot.AssertWritableFields(rc, keysOf(raw), permission.JsonObject(existing.Data), true); !d.Allowed {
		return updated{}, forbiddenWithField(d)
	}

	// The state field can never change through this path (only create and transition move it), so
	// it is always reset to its existing value.
	merged := make(JsonObject)
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range raw {
		merged[k] = v
	}
	if entity.Workflow != nil {
		if existingState, ok := existing.Data[entity.Workflow.StateField]; ok {
			merged[entity.Workflow.StateField] = existingState
		}
	}

	data, fieldErrors := ValidatePayload(entity, merged)
	if fieldErrors != nil {
		return updated{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fieldErrors)
	}
	recomputeFields(entity, data)

	// The record as it will be must still be reachable: an update cannot move a record out of the
	// caller's scopes (nor into one they have no update right in).
	if len(rc.ScopedRoles) > 0 {
		if d := snapshot.CanAccessRecord(rc, entity.Name, entity.Scopes, existing.ID.String(), permission.JsonObject(data), permission.ActionUpdate, ""); !d.Allowed {
			return updated{}, forbidden(d)
		}
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return updated{}, err
	}
	return updated{data: data, dataJSON: dataJSON, code: codeOf(data)}, nil
}
