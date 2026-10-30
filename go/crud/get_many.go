package crud

import (
	"context"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
)

// GetManyResult is one row of CrudService.GetMany's result.
type GetManyResult struct {
	ID           uuid.UUID
	Record       RecordDto
	Capabilities RecordCapabilities
}

// GetMany mirrors the Rust CrudService::get_many — batched counterpart to Get. An id that
// doesn't exist, or that record-level permission denies, is simply absent from the result — not
// an error.
func (s *CrudService) GetMany(ctx context.Context, entityName string, ids []uuid.UUID, rc *permission.RequestContext) (ServiceResult[[]GetManyResult], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[[]GetManyResult]{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[[]GetManyResult]{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return ResultErr[[]GetManyResult](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[[]GetManyResult]{}, err
	}
	if !decision.Allowed {
		return forbidden[[]GetManyResult](decision), nil
	}

	if len(ids) == 0 {
		return ResultOk([]GetManyResult{}), nil
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[[]GetManyResult](err); ok {
			return r, nil
		}
		return ServiceResult[[]GetManyResult]{}, err
	}
	existing, err := fetchExistingBatch(ctx, tx, ids, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[[]GetManyResult]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[[]GetManyResult]{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[[]GetManyResult]{}, err
	}

	byID := map[uuid.UUID]GetManyResult{}
	for _, record := range existing {
		enriched, err := s.enrichRecordForActions(ctx, entity, snapshot,
			[]permission.EntityAction{permission.ActionRead, permission.ActionUpdate, permission.ActionTransition, permission.ActionDelete},
			tenantID, registry, record.Data)
		if err != nil {
			return ServiceResult[[]GetManyResult]{}, err
		}
		recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionRead)
		if !recordDecision.Allowed {
			continue
		}
		capabilities := computeCapabilities(entity, rc, snapshot, enriched)
		id := record.ID
		masked := maskRecordForRead(entity, rc, snapshot, record)
		byID[id] = GetManyResult{ID: id, Record: masked, Capabilities: capabilities}
	}

	ordered := make([]GetManyResult, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}

	return ResultOk(ordered), nil
}
