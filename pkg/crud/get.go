package crud

import (
	"context"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

type GetResult struct {
	Record       RecordDto
	Capabilities RecordCapabilities
}

// Get mirrors the Rust CrudService::get.
func (s *CrudService) Get(ctx context.Context, entityName string, id uuid.UUID, rc *permission.RequestContext) (GetResult, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return GetResult{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return GetResult{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return GetResult{}, NewServiceError(404, "entity_not_found")
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return GetResult{}, err
	}
	if !decision.Allowed {
		return GetResult{}, forbidden(decision)
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return GetResult{}, r
		}
		return GetResult{}, err
	}
	existing, err := fetchExisting(ctx, tx, id, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return GetResult{}, err
	}
	if existing == nil {
		tx.Rollback(ctx) //nolint:errcheck
		return GetResult{}, NewServiceError(404, "record_not_found")
	}
	var actors map[string]string
	if workflow.UsesWorkflowContext(entity) {
		byRecord, err := workflow.LastActors(ctx, tx, tenantID, entity.Name, []uuid.UUID{id})
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return GetResult{}, err
		}
		actors = byRecord[id]
	}
	if err := tx.Commit(ctx); err != nil {
		return GetResult{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return GetResult{}, err
	}
	// Enriched once for every record-level action Get cares about (the Read check below, plus
	// Update/Transition/Delete inside computeCapabilities).
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot,
		[]permission.EntityAction{permission.ActionRead, permission.ActionUpdate, permission.ActionTransition, permission.ActionDelete},
		tenantID, registry, existing.Data)
	if err != nil {
		return GetResult{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionRead)
	if !recordDecision.Allowed {
		return GetResult{}, forbidden(recordDecision)
	}

	capabilities := computeCapabilities(entity, rc, snapshot, enriched, actors)
	masked := maskRecordForRead(entity, rc, snapshot, *existing)
	return GetResult{Record: masked, Capabilities: capabilities}, nil
}
