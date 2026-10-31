package crud

import (
	"context"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
)

// CheckRecordPermission checks whether rc may perform action (Read/Update/Delete only) against
// an *existing* record — entity-level then record-level (ABAC) — without reading, writing, or
// returning the record itself. Exists for a resource that isn't itself a metadata-driven entity
// but is attached to one (attachments, workflow-events history).
func (s *CrudService) CheckRecordPermission(ctx context.Context, entityName string, id uuid.UUID, action permission.EntityAction, rc *permission.RequestContext) (ServiceResult[struct{}], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[struct{}]{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[struct{}]{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return ResultErr[struct{}](404, "entity_not_found"), nil
	}

	var decision permission.PermissionDecision
	switch action {
	case permission.ActionRead:
		decision, err = s.permissions.CanReadEntity(ctx, rc, entity.Name)
	case permission.ActionUpdate:
		decision, err = s.permissions.CanUpdateEntity(ctx, rc, entity.Name)
	case permission.ActionDelete:
		decision, err = s.permissions.CanDeleteEntity(ctx, rc, entity.Name)
	default:
		panic("CheckRecordPermission only supports Read/Update/Delete")
	}
	if err != nil {
		return ServiceResult[struct{}]{}, err
	}
	if !decision.Allowed {
		return forbidden[struct{}](decision), nil
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[struct{}](err); ok {
			return r, nil
		}
		return ServiceResult[struct{}]{}, err
	}
	existing, err := fetchExisting(ctx, tx, id, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[struct{}]{}, err
	}
	if existing == nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ResultErr[struct{}](404, "record_not_found"), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[struct{}]{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[struct{}]{}, err
	}
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot, []permission.EntityAction{action}, tenantID, registry, existing.Data)
	if err != nil {
		return ServiceResult[struct{}]{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), action)
	if !recordDecision.Allowed {
		return forbidden[struct{}](recordDecision), nil
	}

	return ResultOk(struct{}{}), nil
}
