package crud

import (
	"context"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
)

type GetResult struct {
	Record       RecordDto
	Capabilities RecordCapabilities
}

// Get mirrors the Rust CrudService::get.
func (s *CrudService) Get(ctx context.Context, entityName string, id uuid.UUID, rc *permission.RequestContext) (ServiceResult[GetResult], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[GetResult]{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[GetResult]{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return ResultErr[GetResult](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[GetResult]{}, err
	}
	if !decision.Allowed {
		return forbidden[GetResult](decision), nil
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[GetResult](err); ok {
			return r, nil
		}
		return ServiceResult[GetResult]{}, err
	}
	existing, err := fetchExisting(ctx, tx, id, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[GetResult]{}, err
	}
	if existing == nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ResultErr[GetResult](404, "record_not_found"), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[GetResult]{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[GetResult]{}, err
	}
	// Enriched once for every record-level action Get cares about (the Read check below, plus
	// Update/Transition/Delete inside computeCapabilities).
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot,
		[]permission.EntityAction{permission.ActionRead, permission.ActionUpdate, permission.ActionTransition, permission.ActionDelete},
		tenantID, registry, existing.Data)
	if err != nil {
		return ServiceResult[GetResult]{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionRead)
	if !recordDecision.Allowed {
		return forbidden[GetResult](recordDecision), nil
	}

	capabilities := computeCapabilities(entity, rc, snapshot, enriched)
	masked := maskRecordForRead(entity, rc, snapshot, *existing)
	return ResultOk(GetResult{Record: masked, Capabilities: capabilities}), nil
}
