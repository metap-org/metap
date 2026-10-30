package crud

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap-go/audit"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/workflow"
)

// Delete mirrors the Rust CrudService::delete.
func (s *CrudService) Delete(ctx context.Context, entityName string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (ServiceResult[RecordDto], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return ResultErr[RecordDto](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanDeleteEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	if !decision.Allowed {
		return forbidden[RecordDto](decision), nil
	}

	precheckTx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[RecordDto](err); ok {
			return r, nil
		}
		return ServiceResult[RecordDto]{}, err
	}
	existing, err := fetchExisting(ctx, precheckTx, id, tenantID, entity)
	if err != nil {
		precheckTx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if existing == nil {
		precheckTx.Rollback(ctx) //nolint:errcheck
		return ResultErr[RecordDto](404, "record_not_found"), nil
	}
	if err := precheckTx.Commit(ctx); err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	enriched, err := s.enrichRecordForActions(ctx, entity, snapshot, []permission.EntityAction{permission.ActionDelete}, tenantID, registry, existing.Data)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}
	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(enriched), permission.ActionDelete)
	if !recordDecision.Allowed {
		return forbidden[RecordDto](recordDecision), nil
	}

	userID, err := parseUserID(rc)
	if err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[RecordDto](err); ok {
			return r, nil
		}
		return ServiceResult[RecordDto]{}, err
	}

	// Reference-integrity guard: deleting a record that another record still points to via a
	// Reference field would otherwise leave a silent orphan reference. Checked inside the same
	// transaction as the delete itself, right before it.
	refs := referencingFields(registry, entity.Name)
	blockers, err := findReferencingRecords(ctx, tx, tenantID, id, refs)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if len(blockers) > 0 {
		tx.Rollback(ctx) //nolint:errcheck
		first := blockers[0]
		// Keyed "<entity>.<field>" (not just the field name) so the same field name on two
		// different referencing entities doesn't collide — value is each blocker's own record id.
		fieldErrors := map[string][]string{}
		for _, hit := range blockers {
			key := hit.entity + "." + hit.field
			fieldErrors[key] = append(fieldErrors[key], hit.id.String())
		}
		return ResultErrWithMessageAndFieldErrors[RecordDto](409, "record_referenced",
			fmt.Sprintf("This record is referenced by %q on %q and cannot be deleted.", first.field, first.entity),
			fieldErrors), nil
	}

	deleteSQL := fmt.Sprintf(
		`UPDATE %s SET deleted = true, version = version + 1, updated_at = now(), updated_by = $1
		 WHERE id = $2 AND tenant_id = $3 AND version = $4 AND deleted = false RETURNING %s`,
		entity.TableName, recordColumns,
	)
	row := tx.QueryRow(ctx, deleteSQL, userID, id, tenantID, expectedVersion)
	record, err := scanRecordRow(row, entity.Name)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		if errors.Is(err, pgx.ErrNoRows) {
			return ResultErr[RecordDto](409, "version_conflict"), nil
		}
		return ServiceResult[RecordDto]{}, err
	}

	if err := workflow.EmitDeleted(ctx, tx, entity, tenantID, record.ID); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[RecordDto]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[RecordDto]{}, err
	}

	versionAfter := record.Version
	s.recordAudit(ctx, entity, audit.Entry{
		TenantID:    tenantID,
		Entity:      entity.Name,
		RecordID:    record.ID,
		Action:      audit.ActionDelete,
		ActorUserID: userID,
		Reason:      reason,
		// Action-based, not diff-based — RecordDto never exposes the deleted column.
		Diff:         audit.JsonObject{},
		VersionAfter: &versionAfter,
		OccurredAt:   time.Now().UTC(),
	})

	return ResultOk(maskRecordForRead(entity, rc, snapshot, record)), nil
}
