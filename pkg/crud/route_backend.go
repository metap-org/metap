package crud

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// RouteBackends is a RecordBackend that sends each call for an entity in byEntity to that
// entity's backend and everything else to fallback — how a service serves an entity whose records
// live outside its own database (a read model over another system) next to its own.
func RouteBackends(fallback RecordBackend, byEntity map[string]RecordBackend) RecordBackend {
	if len(byEntity) == 0 {
		return fallback
	}
	return &routedBackend{fallback: fallback, byEntity: byEntity}
}

type routedBackend struct {
	fallback RecordBackend
	byEntity map[string]RecordBackend
}

func (b *routedBackend) forEntity(entity string) RecordBackend {
	if be, ok := b.byEntity[entity]; ok {
		return be
	}
	return b.fallback
}

func (b *routedBackend) List(ctx context.Context, entity string, input query.ListInput, rc *permission.RequestContext) (ListResult, error) {
	return b.forEntity(entity).List(ctx, entity, input, rc)
}

func (b *routedBackend) Get(ctx context.Context, entity string, id uuid.UUID, rc *permission.RequestContext) (GetResult, error) {
	return b.forEntity(entity).Get(ctx, entity, id, rc)
}

func (b *routedBackend) GetMany(ctx context.Context, entity string, ids []uuid.UUID, rc *permission.RequestContext) ([]GetManyResult, error) {
	return b.forEntity(entity).GetMany(ctx, entity, ids, rc)
}

func (b *routedBackend) Create(ctx context.Context, entity string, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	return b.forEntity(entity).Create(ctx, entity, data, rc, reason)
}

func (b *routedBackend) Update(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	return b.forEntity(entity).Update(ctx, entity, id, expectedVersion, data, rc, reason)
}

func (b *routedBackend) Transition(ctx context.Context, entity string, id uuid.UUID, action string, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	return b.forEntity(entity).Transition(ctx, entity, id, action, expectedVersion, data, rc, reason)
}

func (b *routedBackend) Delete(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	return b.forEntity(entity).Delete(ctx, entity, id, expectedVersion, rc, reason)
}

func (b *routedBackend) Aggregate(ctx context.Context, entity string, spec query.AggregateSpec, rc *permission.RequestContext) ([]json.RawMessage, error) {
	return b.forEntity(entity).Aggregate(ctx, entity, spec, rc)
}
