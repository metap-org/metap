package crud

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

func (s *CrudService) Create(ctx context.Context, entityName string, rawData JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return RecordDto{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return RecordDto{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return RecordDto{}, NewServiceError(http.StatusNotFound, errcode.EntityNotFound)
	}

	decision, err := s.permissions.CanCreateEntity(ctx, rc, entity.Name)
	if err != nil {
		return RecordDto{}, err
	}
	if !decision.Allowed {
		return RecordDto{}, forbidden(decision)
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return RecordDto{}, err
	}

	keys := make([]string, 0, len(rawData))
	for k := range rawData {
		keys = append(keys, k)
	}
	writeDecision := snapshot.AssertWritableFields(rc, keys, nil, false)
	if !writeDecision.Allowed {
		return RecordDto{}, forbiddenWithField(writeDecision)
	}

	if fe := rejectSystemFields(entity, rawData); fe != nil {
		return RecordDto{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fe)
	}
	data, fieldErrors := ValidatePayload(entity, rawData)
	if fieldErrors != nil {
		return RecordDto{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fieldErrors)
	}

	recomputeFields(entity, data)

	// A caller whose role is scoped may create only inside their scopes (roadmap 115 M1).
	if d := snapshot.CanCreateInScope(rc, entity.Name, entity.Scopes, permission.JsonObject(data)); !d.Allowed {
		return RecordDto{}, forbidden(d)
	}

	status, hasStatus := workflow.GetInitialStatus(entity, data)
	// The state field has to be written into data explicitly here — otherwise the top-level
	// status column and the data blob disagree the moment a caller omits it, which
	// maskRecordForRead's masking check would then read as "absent".
	if entity.Workflow != nil && hasStatus {
		if _, ok := data[entity.Workflow.StateField]; !ok {
			b, _ := json.Marshal(status)
			data[entity.Workflow.StateField] = b
		}
	}
	var code *string
	if raw, ok := data["code"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			code = &s
		}
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return RecordDto{}, err
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return RecordDto{}, r
		}
		return RecordDto{}, err
	}

	var statusArg *string
	if hasStatus {
		statusArg = &status
	} else {
		statusArg = plainStatusValue(entity, data)
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return RecordDto{}, err
	}
	row := tx.QueryRow(ctx, buildInsertSQL(entity), tenantID, code, statusArg, userID, userID, json.RawMessage(dataJSON))
	record, err := scanRecordRow(row, entity.Name)
	if err != nil {
		if r, ok := uniqueViolation(entity, err); ok {
			tx.Rollback(ctx) //nolint:errcheck
			return RecordDto{}, r
		}
		tx.Rollback(ctx) //nolint:errcheck
		return RecordDto{}, err
	}

	if err := workflow.EmitCreated(ctx, tx, entity, tenantID, record.ID, data); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return RecordDto{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordDto{}, err
	}

	versionAfter := record.Version
	s.recordAudit(ctx, entity, record.Data, audit.Entry{
		TenantID:    tenantID,
		Entity:      entity.Name,
		RecordID:    record.ID,
		Action:      audit.ActionCreate,
		ActorUserID: userID,
		Reason:      reason,
		// No real "before" state for a create — diffed against an empty object so every field
		// reads as {"before": null, "after": value}.
		Diff:         audit.DiffJSONObjects(audit.JsonObject{}, audit.JsonObject(data)),
		VersionAfter: &versionAfter,
		OccurredAt:   time.Now().UTC(),
	})

	return maskRecordForRead(entity, rc, snapshot, record), nil
}
