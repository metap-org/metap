package crud

import (
	"context"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

// GetManyResult is one row of CrudService.GetMany's result.
type GetManyResult struct {
	ID           uuid.UUID
	Record       RecordDto
	Capabilities RecordCapabilities
}

// An id that doesn't exist, or that record-level permission denies, is simply absent from the
// result — not an error.
func (s *CrudService) GetMany(ctx context.Context, entityName string, ids []uuid.UUID, rc *permission.RequestContext) ([]GetManyResult, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return nil, NewServiceError(404, "entity_not_found")
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return nil, err
	}
	if !decision.Allowed {
		return nil, forbidden(decision)
	}

	if len(ids) == 0 {
		return []GetManyResult{}, nil
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return nil, r
		}
		return nil, err
	}
	existing, err := fetchExistingBatch(ctx, tx, ids, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return nil, err
	}
	// One workflow_events query for the whole batch, and only when a guard reads `$workflow.`.
	var actorsByRecord map[uuid.UUID]map[string]string
	if workflow.UsesWorkflowContext(entity) {
		recordIDs := make([]uuid.UUID, 0, len(existing))
		for _, record := range existing {
			recordIDs = append(recordIDs, record.ID)
		}
		actorsByRecord, err = workflow.LastActors(ctx, tx, tenantID, entity.Name, recordIDs)
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return nil, err
	}

	byID := map[uuid.UUID]GetManyResult{}
	for _, record := range existing {
		enriched, err := s.enrichRecordForActions(ctx, entity, snapshot,
			[]permission.EntityAction{permission.ActionRead, permission.ActionUpdate, permission.ActionTransition, permission.ActionDelete},
			tenantID, registry, record.Data)
		if err != nil {
			return nil, err
		}
		recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionRead)
		if !recordDecision.Allowed {
			continue
		}
		capabilities := computeCapabilities(entity, rc, snapshot, enriched, actorsByRecord[record.ID])
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

	return ordered, nil
}
