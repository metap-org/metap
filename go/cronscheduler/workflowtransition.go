// "workflow_transition"/"bulk_query_action" targets — both call back into the owning app's
// entity CRUD via the configured crud.RecordBackend (normally a grpcapi.GrpcBackend), reusing
// its permission/validation/audit rather than this binary linking metap-crud/metap-metadata
// directly, which would give an ops binary business-entity knowledge. Ported from
// metap-cron-scheduler's executor/workflow_transition.rs.
package cronscheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
)

// serviceContext is a client-side RequestContext for a call this process makes on its own
// behalf, not on behalf of a live inbound request — ForwardedBearerToken is left empty so
// GrpcBackend always signs with the executor's own ServiceToken; every other field is unused,
// since the remote RecordService derives its own RequestContext from the bearer token's claims
// and never reads what a caller sends here.
func serviceContext() *permission.RequestContext {
	return &permission.RequestContext{}
}

// unwrapResult unwraps a crud.ServiceResult into a plain error, since every caller in this file
// already treats a business-level error (permission denied, validation failed, version
// conflict, ...) the same way it treats a transport-level one: fail this job/step with a
// message, nothing typed to recover from downstream.
func unwrapResult[T any](result crud.ServiceResult[T], err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	if result.IsOk {
		return result.Data, nil
	}
	msg := result.ErrorCode
	if result.Message != nil {
		msg = fmt.Sprintf("%s: %s", result.ErrorCode, *result.Message)
	}
	if result.FieldErrors != nil {
		msg = fmt.Sprintf("%s (field_errors: %v)", msg, result.FieldErrors)
	}
	var zero T
	return zero, fmt.Errorf("%d %s", result.Status, msg)
}

func targetGRPCBackend(backend crud.RecordBackend) (crud.RecordBackend, error) {
	if backend == nil {
		return nil, fmt.Errorf("CRON_TARGET_GRPC_ADDR is unset or the initial connection to it failed — workflow_transition/bulk_query_action jobs cannot run")
	}
	return backend, nil
}

type workflowTransitionConfig struct {
	Entity   string    `json:"entity"`
	RecordID uuid.UUID `json:"recordId"`
	Action   string    `json:"action"`
}

// RunWorkflowTransition runs one "workflow_transition" target firing (or step).
func RunWorkflowTransition(ctx context.Context, backend crud.RecordBackend, targetConfig json.RawMessage) (json.RawMessage, error) {
	var cfg workflowTransitionConfig
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, err
	}
	record, err := transitionOne(ctx, backend, cfg.Entity, cfg.RecordID, cfg.Action)
	if err != nil {
		return nil, err
	}
	return json.Marshal(record)
}

type bulkQueryActionConfig struct {
	Entity string            `json:"entity"`
	Filter map[string]string `json:"filter,omitempty"`
	Action string            `json:"action"`
}

// RunBulkQueryAction runs one "bulk_query_action" target firing (or step): lists every record of
// cfg.Entity matching cfg.Filter (same metadata-constrained query path every other caller of
// ListInput uses), then transitions each one with cfg.Action, collecting per-record failures
// rather than failing the whole firing on the first one.
func RunBulkQueryAction(ctx context.Context, backend crud.RecordBackend, targetConfig json.RawMessage) (json.RawMessage, error) {
	var cfg bulkQueryActionConfig
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, err
	}
	b, err := targetGRPCBackend(backend)
	if err != nil {
		return nil, err
	}

	var filters [][2]string
	for k, v := range cfg.Filter {
		filters = append(filters, [2]string{k, v})
	}
	input := query.ListInput{Limit: 200, Filters: filters}
	records, err := unwrapResult(b.List(ctx, cfg.Entity, input, serviceContext()))
	if err != nil {
		return nil, err
	}

	succeeded := 0
	failed := make([]map[string]any, 0)
	for _, record := range records {
		if _, terr := transitionOne(ctx, backend, cfg.Entity, record.ID, cfg.Action); terr != nil {
			failed = append(failed, map[string]any{"id": record.ID, "error": terr.Error()})
		} else {
			succeeded++
		}
	}

	return json.Marshal(map[string]any{"matched": len(records), "succeeded": succeeded, "failed": failed})
}

// transitionOne is get-then-transition: the transition RPC requires the record's current
// version for optimistic locking, so this always reads first — same two-step every other
// transition caller (the frontend included) has to do.
func transitionOne(ctx context.Context, backend crud.RecordBackend, entity string, recordID uuid.UUID, action string) (crud.RecordDto, error) {
	b, err := targetGRPCBackend(backend)
	if err != nil {
		return crud.RecordDto{}, err
	}
	rc := serviceContext()
	getResult, err := unwrapResult(b.Get(ctx, entity, recordID, rc))
	if err != nil {
		return crud.RecordDto{}, err
	}
	return unwrapResult(b.Transition(ctx, entity, recordID, action, getResult.Record.Version, nil, rc, nil))
}
