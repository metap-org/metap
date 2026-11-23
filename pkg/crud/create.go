package crud

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

// Create writes a new record: resolve, authorize the entity, prepare the data, persist.
func (s *CrudService) Create(ctx context.Context, entityName string, rawData JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	c, err := s.resolve(ctx, rc, entityName)
	if err != nil {
		return RecordDto{}, err
	}
	if err := s.authorizeEntity(ctx, c, permission.ActionCreate, ""); err != nil {
		return RecordDto{}, err
	}
	snapshot, err := s.permissions.LoadSnapshot(ctx, c.tenantID, c.entity.Name)
	if err != nil {
		return RecordDto{}, err
	}
	p, err := prepareCreate(c.entity, snapshot, rc, rawData)
	if err != nil {
		return RecordDto{}, err
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return RecordDto{}, err
	}
	return s.persist(ctx, c, snapshot, write{
		userID: userID, reason: reason,
		sql:    buildInsertSQL(c.entity),
		args:   []any{c.tenantID, p.code, p.status, userID, userID, json.RawMessage(p.dataJSON)},
		unique: true,
		emit: func(ctx context.Context, tx pgx.Tx, record RecordDto) error {
			return workflow.EmitCreated(ctx, tx, c.entity, c.tenantID, record.ID, p.data)
		},
		auditAction: audit.ActionCreate,
		// No real "before" state for a create: diffed against an empty object so every field
		// reads as {"before": null, "after": value}.
		diff: func(RecordDto) audit.JsonObject {
			return audit.DiffJSONObjects(audit.JsonObject{}, audit.JsonObject(p.data))
		},
	})
}

// created is what prepareCreate decides to write.
type created struct {
	data     JsonObject
	dataJSON []byte
	status   *string // the status column: the workflow's initial state, or a plain status value
	code     *string
}

// prepareCreate turns the caller's payload into the row to insert, or says why it cannot. No I/O.
func prepareCreate(entity metadata.EntityDefinition, snapshot *permission.PermissionSnapshot, rc *permission.RequestContext, raw JsonObject) (created, error) {
	if d := snapshot.AssertWritableFields(rc, keysOf(raw), nil, false); !d.Allowed {
		return created{}, forbiddenWithField(d)
	}
	if fe := rejectSystemFields(entity, raw); fe != nil {
		return created{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fe)
	}
	data, fieldErrors := ValidatePayload(entity, raw)
	if fieldErrors != nil {
		return created{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fieldErrors)
	}
	recomputeFields(entity, data)

	// A caller whose role is scoped may create only inside their scopes (roadmap 115 M1).
	if d := snapshot.CanCreateInScope(rc, entity.Name, entity.Scopes, permission.JsonObject(data)); !d.Allowed {
		return created{}, forbidden(d)
	}

	status, hasStatus := workflow.GetInitialStatus(entity, data)
	// The state field has to be written into data explicitly here: otherwise the top-level status
	// column and the data blob disagree the moment a caller omits it, which maskRecordForRead's
	// masking check would then read as "absent".
	if entity.Workflow != nil && hasStatus {
		if _, ok := data[entity.Workflow.StateField]; !ok {
			b, _ := json.Marshal(status)
			data[entity.Workflow.StateField] = b
		}
	}
	var statusArg *string
	if hasStatus {
		statusArg = &status
	} else {
		statusArg = plainStatusValue(entity, data)
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return created{}, err
	}
	return created{data: data, dataJSON: dataJSON, status: statusArg, code: codeOf(data)}, nil
}

// keysOf lists a payload's field names.
func keysOf(data JsonObject) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys
}

// codeOf reads the record's "code" from its data, when it is a string.
func codeOf(data JsonObject) *string {
	if raw, ok := data["code"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return &s
		}
	}
	return nil
}
