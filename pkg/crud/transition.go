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

// Transition moves a record along its workflow: resolve, authorize the entity for this action, load
// the record, authorize it, prepare the next state (guard, validation, validator), persist.
func (s *CrudService) Transition(ctx context.Context, entityName string, id uuid.UUID, action string, expectedVersion int32,
	payload JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	c, err := s.resolve(ctx, rc, entityName)
	if err != nil {
		return RecordDto{}, err
	}
	// Per-action entity check: `transition` covers every action, `transition:<action>` only this
	// one (permission.TransitionActionPolicy).
	if err := s.authorizeEntity(ctx, c, permission.ActionTransition, action); err != nil {
		return RecordDto{}, err
	}
	cur, err := s.loadCurrent(ctx, c, id, true)
	if err != nil {
		return RecordDto{}, err
	}
	snapshot, err := s.authorizeRecord(ctx, c, cur, permission.ActionTransition, action)
	if err != nil {
		return RecordDto{}, err
	}
	p, err := prepareTransition(c.entity, snapshot, rc, cur, action, payload)
	if err != nil {
		return RecordDto{}, err
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return RecordDto{}, err
	}

	transitionAction := action
	return s.persist(ctx, c, snapshot, write{
		previous: cur.record.Data,
		userID:   userID, reason: reason,
		sql:       buildUpdateSQL(c.entity, "status = $6::varchar"),
		args:      []any{json.RawMessage(p.dataJSON), userID, id, c.tenantID, expectedVersion, p.toState},
		versioned: true,
		emit: func(ctx context.Context, tx pgx.Tx, record RecordDto) error {
			if err := workflow.RecordEvent(ctx, tx, c.entity, record.ID, action, p.fromState, p.toState, rc); err != nil {
				return err
			}
			return workflow.EmitTransitioned(ctx, tx, c.entity, c.tenantID, record.ID, action, p.fromState, p.toState, userID)
		},
		auditAction:      audit.ActionTransition,
		transitionAction: &transitionAction,
		// record.Data (read back from the row this query just wrote), not the local data.
		diff: func(record RecordDto) audit.JsonObject {
			return audit.DiffJSONObjects(audit.JsonObject(cur.record.Data), audit.JsonObject(record.Data))
		},
	})
}

// transitioned is what prepareTransition decides to write.
type transitioned struct {
	dataJSON  []byte
	fromState string
	toState   string
}

// prepareTransition checks the move is allowed from the record's state (guard, validation,
// validator) and builds the data for the new state. No I/O.
func prepareTransition(entity metadata.EntityDefinition, snapshot *permission.PermissionSnapshot, rc *permission.RequestContext,
	cur *current, action string, payload JsonObject) (transitioned, error) {
	if entity.Workflow == nil {
		return transitioned{}, NewServiceError(http.StatusBadRequest, "no_workflow")
	}
	workflowDef := entity.Workflow
	existing := cur.record

	fromRaw, ok := existing.Data[workflowDef.StateField]
	if !ok {
		return transitioned{}, NewServiceError(http.StatusConflict, errcode.InvalidTransition)
	}
	var fromState string
	if json.Unmarshal(fromRaw, &fromState) != nil {
		return transitioned{}, NewServiceError(http.StatusConflict, errcode.InvalidTransition)
	}

	transition := workflow.FindTransition(entity, action, fromState)
	if transition == nil {
		return transitioned{}, NewServiceError(http.StatusConflict, errcode.InvalidTransition)
	}

	if guardErr := workflow.RunGuard(transition, workflow.WithWorkflowContext(existing.Data, cur.actors), rc); guardErr != nil {
		message, details := workflow.Explain(guardErr, &entity)
		return transitioned{}, &ServiceError{Status: http.StatusUnprocessableEntity, Code: "guard_failed", Message: message, Details: details}
	}
	toState := transition.To

	// A transition's payload goes through the same writable-fields check and field-metadata
	// validation as Update; the state field itself is always driven by toState, never by the caller.
	merged := make(JsonObject)
	for k, v := range existing.Data {
		merged[k] = v
	}
	if payload != nil {
		if d := snapshot.AssertWritableFields(rc, keysOf(payload), permission.JsonObject(existing.Data), true); !d.Allowed {
			return transitioned{}, forbiddenWithField(d)
		}
		for k, v := range payload {
			merged[k] = v
		}
	}
	toStateJSON, _ := json.Marshal(toState)
	merged[workflowDef.StateField] = toStateJSON

	nextData, fieldErrors := ValidatePayload(entity, merged)
	if fieldErrors != nil {
		return transitioned{}, NewServiceErrorFields(http.StatusBadRequest, errcode.ValidationFailed, fieldErrors)
	}

	if validatorErr := workflow.RunValidator(transition, workflow.WithWorkflowContext(nextData, cur.actors), rc); validatorErr != nil {
		message, details := workflow.Explain(validatorErr, &entity)
		return transitioned{}, &ServiceError{Status: http.StatusUnprocessableEntity, Code: "validator_failed", Message: message, Details: details}
	}

	workflow.ApplySetFields(transition, nextData, rc)

	dataJSON, err := json.Marshal(nextData)
	if err != nil {
		return transitioned{}, err
	}
	return transitioned{dataJSON: dataJSON, fromState: fromState, toState: toState}, nil
}
