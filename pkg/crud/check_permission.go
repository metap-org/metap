package crud

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// CheckRecordPermission checks whether rc may perform action (Read/Update/Delete only) against
// an *existing* record — entity-level then record-level (ABAC) — without reading, writing, or
// returning the record itself. Exists for a resource that isn't itself a metadata-driven entity
// but is attached to one (attachments, workflow-events history).
func (s *CrudService) CheckRecordPermission(ctx context.Context, entityName string, id uuid.UUID, action permission.EntityAction, rc *permission.RequestContext) error {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return NewServiceError(http.StatusNotFound, errcode.EntityNotFound)
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
		return err
	}
	if !decision.Allowed {
		return forbidden(decision)
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return r
		}
		return err
	}
	existing, err := fetchExisting(ctx, tx, id, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return err
	}
	if existing == nil {
		tx.Rollback(ctx) //nolint:errcheck
		return NewServiceError(http.StatusNotFound, errcode.RecordNotFound)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return err
	}
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot, []permission.EntityAction{action}, tenantID, registry, existing.Data)
	if err != nil {
		return err
	}
	recordDecision := snapshot.CanAccessRecord(rc, entity.Name, entity.Scopes, existing.ID.String(), permission.JsonObject(enriched), action, "")
	if !recordDecision.Allowed {
		return forbidden(recordDecision)
	}

	return nil
}
