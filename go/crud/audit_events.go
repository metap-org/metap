package crud

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/audit"
	"github.com/metap-org/metap-go/permission"
)

// ListAuditEvents is the read side of recordAudit — full create/update/delete/transition
// history for one record. Record-level (ABAC) read permission via CheckRecordPermission.
// Returns whatever rows exist regardless of the entity's *current* EntityAuditConfig.Enabled
// flag — toggling audit off must not make already-recorded history disappear.
func (s *CrudService) ListAuditEvents(ctx context.Context, entityName string, recordID uuid.UUID, rc *permission.RequestContext) (ServiceResult[[]audit.Row], error) {
	check, err := s.CheckRecordPermission(ctx, entityName, recordID, permission.ActionRead, rc)
	if err != nil {
		return ServiceResult[[]audit.Row]{}, err
	}
	if !check.IsOk {
		return resultErrFrom[struct{}, []audit.Row](check), nil
	}

	if s.auditStore == nil {
		return ResultOk([]audit.Row{}), nil
	}
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[[]audit.Row]{}, err
	}
	events, err := s.auditStore.ListForRecord(ctx, tenantID, entityName, recordID)
	if err != nil {
		return ServiceResult[[]audit.Row]{}, err
	}

	visibility, err := s.auditDiffVisibility(ctx, entityName, recordID, rc)
	if err != nil {
		return ServiceResult[[]audit.Row]{}, err
	}
	for i := range events {
		maskDiff(&events[i].Diff, visibility)
	}
	return ResultOk(events), nil
}

// diffVisibility is how much of this record's audit diffs the caller may see. nil fields means
// "nothing" (record-level access itself is state-dependent); non-nil names the readable field
// set.
type diffVisibility struct {
	nothing bool
	fields  map[string]bool
}

// auditDiffVisibility: see the Rust CrudService's audit_events.rs doc comment for the full
// reasoning — authorization that depends on mutable record state cannot be used to decide access
// to values describing states the record no longer has, at two levels (record, then field).
func (s *CrudService) auditDiffVisibility(ctx context.Context, entityName string, recordID uuid.UUID, rc *permission.RequestContext) (diffVisibility, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return diffVisibility{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return diffVisibility{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return diffVisibility{nothing: true}, nil
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return diffVisibility{}, err
	}
	existing, err := fetchExisting(ctx, tx, recordID, tenantID, entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return diffVisibility{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return diffVisibility{}, err
	}

	probe := JsonObject{}
	if existing != nil {
		for k, v := range existing.Data {
			probe[k] = v
		}
	}
	for _, field := range entity.Fields {
		if _, ok := probe[field.Name]; !ok {
			probe[field.Name] = json.RawMessage("null")
		}
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return diffVisibility{}, err
	}

	if snapshot.RecordAccessIsStateDependent(rc, permission.ActionRead) {
		return diffVisibility{nothing: true}, nil
	}

	stateDependent := snapshot.RecordStateDependentReadFields(rc)
	readable := snapshot.FilterReadableFields(rc, permission.JsonObject(probe))
	fields := map[string]bool{}
	for field := range readable {
		if !stateDependent[field] {
			fields[field] = true
		}
	}
	return diffVisibility{fields: fields}, nil
}

// maskDiff drops every key of an audit entry's {field: {before, after}} diff the caller may not
// see.
func maskDiff(diff *json.RawMessage, visibility diffVisibility) {
	if visibility.nothing {
		*diff = json.RawMessage("{}")
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(*diff, &m); err != nil {
		*diff = json.RawMessage("{}")
		return
	}
	for field := range m {
		if !visibility.fields[field] {
			delete(m, field)
		}
	}
	b, _ := json.Marshal(m)
	*diff = b
}
