// RecordBackend is the seam that lets a GraphQL resolver (metap-graphql, a later wave) or any
// other generic caller be written once against "however this entity's records actually get read
// and written," without hardcoding whether that means an in-process CrudService call or a
// remote call to another, separately-deployed microservice.
package crud

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
)

// RecordBackend mirrors the Rust RecordBackend trait.
type RecordBackend interface {
	List(ctx context.Context, entity string, input query.ListInput, rc *permission.RequestContext) (ServiceResult[[]RecordDto], error)
	Get(ctx context.Context, entity string, id uuid.UUID, rc *permission.RequestContext) (ServiceResult[GetResult], error)
	GetMany(ctx context.Context, entity string, ids []uuid.UUID, rc *permission.RequestContext) (ServiceResult[[]GetManyResult], error)
	Create(ctx context.Context, entity string, data JsonObject, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error)
	Update(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error)
	Transition(ctx context.Context, entity string, id uuid.UUID, action string, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error)
	Delete(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error)
	// Aggregate is the GROUP BY/COUNT/SUM counterpart to List — see CrudService.Aggregate's own
	// doc comment for the permission/masking rules this must preserve regardless of which
	// backend actually runs it (in-process CrudService, or a remote GrpcBackend call, a later
	// wave).
	Aggregate(ctx context.Context, entity string, spec query.AggregateSpec, rc *permission.RequestContext) (ServiceResult[[]json.RawMessage], error)
}

// Ensure CrudService satisfies RecordBackend — a thin, logic-free delegation, since every other
// method already matches the interface's shape exactly.
var _ RecordBackend = (*CrudService)(nil)

// Aggregate is RecordBackend's shape (taking the raw wire-shape AggregateSpec, since an entity
// isn't known at compile time to any caller reaching this through the interface) — it parses
// spec and delegates to AggregateWithInput, the CrudService-specific method taking the already-
// parsed AggregateInput that List/Get/etc.'s own callers use directly. A malformed spec (bad
// metric/bucket string) surfaces as a 400-shaped ServiceResult, not an error — matching every
// other method's own input-parsing convention.
func (s *CrudService) Aggregate(ctx context.Context, entityName string, spec query.AggregateSpec, rc *permission.RequestContext) (ServiceResult[[]json.RawMessage], error) {
	input, err := spec.IntoInput()
	if err != nil {
		return ResultErrWithMessage[[]json.RawMessage](400, "invalid_aggregate", err.Error()), nil
	}
	return s.AggregateWithInput(ctx, entityName, input, rc)
}
