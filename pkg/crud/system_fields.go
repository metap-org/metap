package crud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/metadata"
)

// rejectSystemFields returns a field error for every System field present in a user payload.
// System fields are written only by SetSystemFields.
func rejectSystemFields(entity metadata.EntityDefinition, data JsonObject) FieldErrors {
	var errs FieldErrors
	for _, name := range metadata.SystemFieldNames(entity) {
		if _, present := data[name]; present {
			if errs == nil {
				errs = FieldErrors{}
			}
			errs[name] = append(errs[name], "system_field")
		}
	}
	return errs
}

// SetSystemFields writes System fields of one record (e.g. an API key's lastUsedAt) for a
// platform-internal caller. Unlike Update it takes no RequestContext and checks no permission —
// it must never be reachable from a request payload — and it deliberately does **not** bump
// version (a user editing the record must not get a version_conflict because the platform
// touched a counter), does not change updated_at/updated_by, emits no outbox event and writes no
// audit entry. Every key must be a declared System field of the entity and pass that field's
// kind validation. Returns found=false when no live record matches.
func (s *CrudService) SetSystemFields(ctx context.Context, tenantID uuid.UUID, entityName string, id uuid.UUID, fields JsonObject) (found bool, err error) {
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return false, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return false, fmt.Errorf("entity %q not found", entityName)
	}
	system := map[string]bool{}
	for _, name := range metadata.SystemFieldNames(entity) {
		system[name] = true
	}
	var declared []metadata.EntityField
	for _, f := range entity.Fields {
		if _, present := fields[f.Name]; present {
			declared = append(declared, f)
		}
	}
	for name := range fields {
		if !system[name] {
			return false, fmt.Errorf("field %q of %q is not a system field", name, entityName)
		}
	}
	validated, fieldErrors := ValidatePayload(metadata.EntityDefinition{Name: entity.Name, Fields: declared}, fields)
	if fieldErrors != nil {
		return false, fmt.Errorf("invalid system field values for %q: %v", entityName, fieldErrors)
	}
	patch, err := json.Marshal(validated)
	if err != nil {
		return false, err
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET data = data || $1::jsonb WHERE id = $2 AND tenant_id = $3 AND deleted = false`, entity.TableName),
		json.RawMessage(patch), id, tenantID)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
