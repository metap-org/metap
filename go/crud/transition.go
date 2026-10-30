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

// Transition mirrors the Rust CrudService::transition.
func (s *CrudService) Transition(ctx context.Context, entityName string, id uuid.UUID, action string, expectedVersion int32,
	payload JsonObject, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error) {
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

	decision, err := s.permissions.CanTransitionEntity(ctx, rc, entity.Name)
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
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot, []permission.EntityAction{permission.ActionTransition}, tenantID, registry, existing.Data)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionTransition)
	if !recordDecision.Allowed {
		return forbidden[RecordDto](recordDecision), nil
	}

	if entity.Workflow == nil {
		return ResultErr[RecordDto](400, "no_workflow"), nil
	}
	workflowDef := entity.Workflow

	fromRaw, ok := existing.Data[workflowDef.StateField]
	if !ok {
		return ResultErr[RecordDto](409, "invalid_transition"), nil
	}
	var fromState string
	if json.Unmarshal(fromRaw, &fromState) != nil {
		return ResultErr[RecordDto](409, "invalid_transition"), nil
	}

	transition := workflow.FindTransition(entity, action, fromState)
	if transition == nil {
		return ResultErr[RecordDto](409, "invalid_transition"), nil
	}

	if guardErr := workflow.RunGuard(transition, existing.Data, rc); guardErr != nil {
		return ResultErrWithMessage[RecordDto](422, "guard_failed", guardErr.Error()), nil
	}
	toState := transition.To

	// A transition's payload goes through the same writable-fields check and field-metadata
	// validation as Update — the state field itself is always driven by toState, never by the
	// caller.
	merged := make(JsonObject, len(existing.Data)+len(payload))
	for k, v := range existing.Data {
		merged[k] = v
	}
	if payload != nil {
		keys := make([]string, 0, len(payload))
		for k := range payload {
			keys = append(keys, k)
		}
		writeDecision := snapshot.AssertWritableFields(rc, keys, permission.JsonObject(existing.Data), true)
		if !writeDecision.Allowed {
			return forbiddenWithField[RecordDto](writeDecision), nil
		}
		for k, v := range payload {
			merged[k] = v
		}
	}
	toStateJSON, _ := json.Marshal(toState)
	merged[workflowDef.StateField] = toStateJSON

	nextData, fieldErrors := ValidatePayload(entity, merged)
	if fieldErrors != nil {
		return ResultErrWithFieldErrors[RecordDto](400, "validation_failed", fieldErrors), nil
	}

	if validatorErr := workflow.RunValidator(transition, nextData, rc); validatorErr != nil {
		return ResultErrWithMessage[RecordDto](422, "validator_failed", validatorErr.Error()), nil
	}

	workflow.ApplySetFields(transition, nextData, rc)

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

	nextDataJSON, err := json.Marshal(nextData)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	transitionSQL := fmt.Sprintf(
		`UPDATE %s SET data = $1, status = $2, version = version + 1, updated_at = now(), updated_by = $3
		 WHERE id = $4 AND tenant_id = $5 AND version = $6 AND deleted = false RETURNING %s`,
		entity.TableName, recordColumns,
	)
	row := tx.QueryRow(ctx, transitionSQL, json.RawMessage(nextDataJSON), toState, userID, id, tenantID, expectedVersion)
	record, err := scanRecordRow(row, entity.Name)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		if errors.Is(err, pgx.ErrNoRows) {
			return ResultErr[RecordDto](409, "version_conflict"), nil
		}
		return ServiceResult[RecordDto]{}, err
	}

	if err := workflow.RecordEvent(ctx, tx, entity, record.ID, action, fromState, toState, rc); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if err := workflow.EmitTransitioned(ctx, tx, entity, tenantID, record.ID, action, fromState, toState, userID); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	versionAfter := record.Version
	transitionAction := action
	s.recordAudit(ctx, entity, audit.Entry{
		TenantID:         tenantID,
		Entity:           entity.Name,
		RecordID:         record.ID,
		Action:           audit.ActionTransition,
		TransitionAction: &transitionAction,
		ActorUserID:      userID,
		Reason:           reason,
		// record.Data (read back from the row this query just wrote), not the local nextData.
		Diff:         audit.DiffJSONObjects(audit.JsonObject(existing.Data), audit.JsonObject(record.Data)),
		VersionAfter: &versionAfter,
		OccurredAt:   time.Now().UTC(),
	})

	return ResultOk(maskRecordForRead(entity, rc, snapshot, record)), nil
}
