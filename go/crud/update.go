package crud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap-go/audit"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/workflow"
)

// Update mirrors the Rust CrudService::update.
func (s *CrudService) Update(ctx context.Context, entityName string, id uuid.UUID, expectedVersion int32, rawData JsonObject, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return ResultErr[RecordDto](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanUpdateEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	if !decision.Allowed {
		return forbidden[RecordDto](decision), nil
	}

	precheckTx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[RecordDto](err); ok {
			return r, nil
		}
		return ServiceResult[RecordDto]{}, err
	}
	existing, err := fetchExisting(ctx, precheckTx, id, tenantID, entity)
	if err != nil {
		precheckTx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if existing == nil {
		precheckTx.Rollback(ctx) //nolint:errcheck
		return ResultErr[RecordDto](404, "record_not_found"), nil
	}
	if err := precheckTx.Commit(ctx); err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot, []permission.EntityAction{permission.ActionUpdate}, tenantID, registry, existing.Data)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionUpdate)
	if !recordDecision.Allowed {
		return forbidden[RecordDto](recordDecision), nil
	}

	keys := make([]string, 0, len(rawData))
	for k := range rawData {
		keys = append(keys, k)
	}
	writeDecision := snapshot.AssertWritableFields(rc, keys, permission.JsonObject(existing.Data), true)
	if !writeDecision.Allowed {
		return forbiddenWithField[RecordDto](writeDecision), nil
	}

	// The state field can never change through this path — only create and transition are
	// allowed to move it — so it's always reset to its existing value.
	merged := make(JsonObject, len(existing.Data)+len(rawData))
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range rawData {
		merged[k] = v
	}
	if entity.Workflow != nil {
		if existingState, ok := existing.Data[entity.Workflow.StateField]; ok {
			merged[entity.Workflow.StateField] = existingState
		}
	}

	data, fieldErrors := ValidatePayload(entity, merged)
	if fieldErrors != nil {
		return ResultErrWithFieldErrors[RecordDto](400, "validation_failed", fieldErrors), nil
	}
	recomputeFields(entity, data)

	var code *string
	if raw, ok := data["code"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			code = &s
		}
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[RecordDto](err); ok {
			return r, nil
		}
		return ServiceResult[RecordDto]{}, err
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	updateSQL := fmt.Sprintf(
		`UPDATE %s SET data = $1, code = $2, version = version + 1, updated_at = now(), updated_by = $3
		 WHERE id = $4 AND tenant_id = $5 AND version = $6 AND deleted = false RETURNING %s`,
		entity.TableName, recordColumns,
	)
	row := tx.QueryRow(ctx, updateSQL, json.RawMessage(dataJSON), code, userID, id, tenantID, expectedVersion)
	record, err := scanRecordRow(row, entity.Name)
	if err != nil {
		if r, ok := uniqueViolation[RecordDto](entity, err); ok {
			tx.Rollback(ctx) //nolint:errcheck
			return r, nil
		}
		tx.Rollback(ctx) //nolint:errcheck
		if errors.Is(err, pgx.ErrNoRows) {
			return ResultErr[RecordDto](409, "version_conflict"), nil
		}
		return ServiceResult[RecordDto]{}, err
	}

	if err := workflow.EmitUpdated(ctx, tx, entity, tenantID, record.ID, data, record.Version); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	s.invalidateContextCacheIfConfigured(entity.Name, tenantID, data)

	versionAfter := record.Version
	s.recordAudit(ctx, entity, audit.Entry{
		TenantID:     tenantID,
		Entity:       entity.Name,
		RecordID:     record.ID,
		Action:       audit.ActionUpdate,
		ActorUserID:  userID,
		Reason:       reason,
		Diff:         audit.DiffJSONObjects(audit.JsonObject(existing.Data), audit.JsonObject(data)),
		VersionAfter: &versionAfter,
		OccurredAt:   time.Now().UTC(),
	})

	return ResultOk(maskRecordForRead(entity, rc, snapshot, record)), nil
}
