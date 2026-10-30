// Shared helpers used across crud's per-operation files — error-response mapping, row<->DTO
// conversion, field/record masking, capability computation, and the delete-time
// reference-integrity guard.
package crud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/workflow"
)

// recordColumns: every entity is a dedicated table-per-entity table — one table already means
// one entity, so there is no "entity" discriminator column to select.
const recordColumns = "id, code, status, data, version, created_at, updated_at"

// recomputeFields fills in every Computed field's value from the rest of data — called from
// create/update after ValidatePayload has already accepted the payload, before the write lands.
// Always overwrites whatever the client sent for a computed field, if anything.
func recomputeFields(entity metadata.EntityDefinition, data JsonObject) {
	for _, field := range entity.Fields {
		if field.Computed == nil {
			continue
		}
		rendered := metadata.RenderExpression(field.Computed.Expression, func(name string) (string, bool) {
			raw, ok := data[name]
			if !ok {
				return "", false
			}
			return computedTokenToString(raw), true
		})
		b, _ := json.Marshal(rendered)
		data[field.Name] = b
	}
}

// computedTokenToString: String values pass through as-is, null/absent renders empty, everything
// else uses Go's default JSON-to-string rendering (acceptable for v1's "string template").
func computedTokenToString(raw json.RawMessage) string {
	if isJSONNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		switch vv := v.(type) {
		case float64:
			return formatFloat(vv)
		case bool:
			if vv {
				return "true"
			}
			return "false"
		default:
			b, _ := json.Marshal(v)
			return string(b)
		}
	}
	return string(raw)
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

func parseUserID(rc *permission.RequestContext) (*uuid.UUID, error) {
	if rc.UserID == "" {
		return nil, nil
	}
	id, err := uuid.Parse(rc.UserID)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func forbidden[T any](decision permission.PermissionDecision) ServiceResult[T] {
	reason := decision.Reason
	if reason == "" {
		reason = "forbidden"
	}
	return ResultErr[T](403, reason)
}

func forbiddenWithField[T any](decision permission.PermissionDecision) ServiceResult[T] {
	reason := decision.Reason
	if reason == "" {
		reason = "forbidden"
	}
	if decision.Field != "" {
		return ResultErrWithFieldErrors[T](403, reason, map[string][]string{decision.Field: {"forbidden"}})
	}
	return ResultErr[T](403, reason)
}

// compositeUniqueIndexName mirrors metap_reconciler::compile::composite_unique_index_name's
// naming exactly for any name that didn't need that function's 63-byte truncate-with-hash
// fallback — duplicated here rather than depended on, same "duplicate the trivial pure naming
// logic" convention metap-peripherals's own index naming already uses.
func compositeUniqueIndexName(prefix string, fields []string) string {
	return prefix + strings.Join(fields, "_")
}

// uniqueViolation maps a Postgres unique-index violation (caught after the fact at the
// INSERT/UPDATE call site) to a 409 naming *what* collided. Returns (zero, false) for any other
// error, so the caller's generic error fallback still applies.
func uniqueViolation[T any](entity metadata.EntityDefinition, err error) (ServiceResult[T], bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ServiceResult[T]{}, false
	}
	constraintName := pgErr.ConstraintName
	if constraintName == "" {
		return unnamedUniqueViolation[T](entity), true
	}

	mangled := strings.ReplaceAll(entity.Name, ".", "_")
	prefix := "uniq_" + mangled + "_"

	for _, constraint := range entity.UniqueConstraints {
		if compositeUniqueIndexName(prefix, constraint.Fields) == constraintName {
			fieldErrors := map[string][]string{}
			for _, f := range constraint.Fields {
				fieldErrors[f] = []string{"A record with this combination of values already exists."}
			}
			return ResultErrWithFieldErrors[T](409, "unique_violation", fieldErrors), true
		}
	}

	if field, ok := strings.CutPrefix(constraintName, prefix); ok {
		return ResultErrWithFieldErrors[T](409, "unique_violation", map[string][]string{
			field: {"A record with this value already exists."},
		}), true
	}
	return unnamedUniqueViolation[T](entity), true
}

func unnamedUniqueViolation[T any](entity metadata.EntityDefinition) ServiceResult[T] {
	return ResultErrWithMessage[T](409, "unique_violation", fmt.Sprintf("A unique constraint was violated on %q.", entity.Name))
}

// routerUnavailable maps a control.RouterError (an expected part of the tenant lifecycle) to a
// clean 4xx/5xx. Returns (zero, false) for any other error, so the caller's generic error
// fallback still applies.
func routerUnavailable[T any](err error) (ServiceResult[T], bool) {
	var routerErr *control.RouterError
	if !errors.As(err, &routerErr) {
		return ServiceResult[T]{}, false
	}
	switch routerErr.Kind {
	case control.ErrTenantSuspended, control.ErrTenantExpired:
		return ResultErr[T](403, "tenant_unavailable"), true
	case control.ErrTenantMigrating, control.ErrTenantProvisioning:
		return ResultErr[T](503, "tenant_unavailable"), true
	case control.ErrTenantDeleted:
		return ResultErr[T](404, "tenant_not_found"), true
	default: // ErrInvalidSchemaName: should never occur from real data — fall through as a real error
		return ServiceResult[T]{}, false
	}
}

// referencingField is one (entity, field) pair delete() needs to check for an orphan reference.
type referencingField struct {
	refEntity     string
	refField      string
	refTable      string
	hasRealColumn bool
}

// referencingFields: every (entity, field) pair across the whole registry where field is a
// Reference kind pointing at targetEntity. Includes self-references.
func referencingFields(registry *metadata.MetadataRegistry, targetEntity string) []referencingField {
	var result []referencingField
	summaries := registry.ListEntities()
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	for _, summary := range summaries {
		for _, field := range summary.Fields {
			if field.Kind == metadata.KindReference && field.RefEntity != nil && *field.RefEntity == targetEntity {
				entityDef, _ := registry.GetEntity(summary.Name)
				result = append(result, referencingField{
					refEntity:     summary.Name,
					refField:      field.Name,
					refTable:      entityDef.TableName,
					hasRealColumn: metadata.FieldHasRealColumn(field),
				})
			}
		}
	}
	return result
}

// referencingRecordHit is one blocking row delete()'s reference-integrity guard found.
type referencingRecordHit struct {
	entity string
	field  string
	id     uuid.UUID
}

// maxReferencingHits bounds the total blocking rows reported across every referencing table
// combined.
const maxReferencingHits = 50

// findReferencingRecords runs one combined query per distinct physical table among refs. Returns
// every blocking row (up to maxReferencingHits total), not just the first.
func findReferencingRecords(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, refs []referencingField) ([]referencingRecordHit, error) {
	var hits []referencingRecordHit
	if len(refs) == 0 {
		return hits, nil
	}

	byTable := map[string][]referencingField{}
	var tables []string
	for _, r := range refs {
		if _, ok := byTable[r.refTable]; !ok {
			tables = append(tables, r.refTable)
		}
		byTable[r.refTable] = append(byTable[r.refTable], r)
	}
	sort.Strings(tables)

	for _, table := range tables {
		if int64(len(hits)) >= maxReferencingHits {
			break
		}
		group := byTable[table]
		remaining := maxReferencingHits - int64(len(hits))

		var clauses []string
		args := []any{tenantID, id}
		for _, r := range group {
			if r.hasRealColumn {
				clauses = append(clauses, fmt.Sprintf(`"%s" = $%d::uuid`, r.refField, len(args)+1))
				args = append(args, id)
			} else {
				clauses = append(clauses, fmt.Sprintf(`data ->> '%s' = $%d`, r.refField, len(args)+1))
				args = append(args, id.String())
			}
		}
		sql := fmt.Sprintf(
			"SELECT id FROM %s WHERE tenant_id = $1 AND deleted = false AND id != $2 AND (%s) LIMIT %d",
			table, strings.Join(clauses, " OR "), remaining,
		)
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rowID uuid.UUID
			if err := rows.Scan(&rowID); err != nil {
				rows.Close()
				return nil, err
			}
			hits = append(hits, referencingRecordHit{entity: group[0].refEntity, field: group[0].refField, id: rowID})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return hits, nil
}

func scanRecordRow(scanner interface{ Scan(...any) error }, entityName string) (RecordDto, error) {
	var dto RecordDto
	var dataRaw json.RawMessage
	if err := scanner.Scan(&dto.ID, &dto.Code, &dto.Status, &dataRaw, &dto.Version, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
		return RecordDto{}, err
	}
	dto.Entity = entityName
	var data JsonObject
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return RecordDto{}, fmt.Errorf("%s's data was not a JSON object: %w", entityName, err)
	}
	dto.Data = data
	return dto, nil
}

func fetchExisting(ctx context.Context, tx pgx.Tx, id, tenantID uuid.UUID, entity metadata.EntityDefinition) (*RecordDto, error) {
	row := tx.QueryRow(ctx, fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = $1 AND tenant_id = $2 AND deleted = false", recordColumns, entity.TableName,
	), id, tenantID)
	dto, err := scanRecordRow(row, entity.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dto, nil
}

// fetchExistingBatch is the batched counterpart to fetchExisting, for get_many — one query for
// every id instead of one fetchExisting call per id. Order is whatever = ANY($1) returns.
func fetchExistingBatch(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, tenantID uuid.UUID, entity metadata.EntityDefinition) ([]RecordDto, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = ANY($1) AND tenant_id = $2 AND deleted = false", recordColumns, entity.TableName,
	), ids, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecordDto
	for rows.Next() {
		dto, err := scanRecordRow(rows, entity.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, rows.Err()
}

// fetchRelatedData is a raw data fetch for one hop of cross-record permission enrichment —
// deliberately no permission check on the related record: this never leaves the server as a
// response, it's only ever fed into PolicyCondition evaluation for the *current* record.
func fetchRelatedData(ctx context.Context, tx pgx.Tx, id, tenantID uuid.UUID, refEntity metadata.EntityDefinition) (JsonObject, error) {
	row := tx.QueryRow(ctx, fmt.Sprintf(
		"SELECT data FROM %s WHERE id = $1 AND tenant_id = $2 AND deleted = false", refEntity.TableName,
	), id, tenantID)
	var dataRaw json.RawMessage
	if err := row.Scan(&dataRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var data JsonObject
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// fetchRelatedRecordsBatch is the batched counterpart to fetchRelatedData, for
// hydrateRelatedDisplay — one query for every id a whole list page needs from a given related
// entity, instead of one query per row. Returns each related record's *whole* data.
func fetchRelatedRecordsBatch(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, tenantID uuid.UUID, refEntity metadata.EntityDefinition) (map[uuid.UUID]JsonObject, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(
		"SELECT id, data FROM %s WHERE id = ANY($1) AND tenant_id = $2 AND deleted = false", refEntity.TableName,
	), ids, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[uuid.UUID]JsonObject{}
	for rows.Next() {
		var id uuid.UUID
		var dataRaw json.RawMessage
		if err := rows.Scan(&id, &dataRaw); err != nil {
			return nil, err
		}
		var data JsonObject
		if err := json.Unmarshal(dataRaw, &data); err == nil {
			result[id] = data
		}
	}
	return result, rows.Err()
}

// maskRecordForRead: the dedicated table's code/status columns mirror data.code/
// data[entity.Workflow.StateField] purely for indexing — FilterReadableFields only masks the
// data blob, so this masks the mirrored top-level columns the same way or a denied field's value
// still leaks through them.
func maskRecordForRead(entity metadata.EntityDefinition, rc *permission.RequestContext, snapshot *permission.PermissionSnapshot, row RecordDto) RecordDto {
	filteredData := snapshot.FilterReadableFields(rc, permission.JsonObject(row.Data))
	row.Data = filteredData
	if _, ok := filteredData["code"]; !ok {
		row.Code = nil
	}
	if entity.Workflow != nil {
		if _, ok := filteredData[entity.Workflow.StateField]; !ok {
			row.Status = nil
		}
	}
	return row
}

func computeCapabilities(entity metadata.EntityDefinition, rc *permission.RequestContext, snapshot *permission.PermissionSnapshot, existingData JsonObject) RecordCapabilities {
	allFieldNames := make([]string, len(entity.Fields))
	for i, f := range entity.Fields {
		allFieldNames[i] = f.Name
	}
	writableFields := snapshot.WritableFields(rc, allFieldNames, permission.JsonObject(existingData), true)

	recordDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(existingData), permission.ActionUpdate)
	canUpdate := recordDecision.Allowed
	transitionDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(existingData), permission.ActionTransition)
	deleteDecision := snapshot.CanPerformRecordCondition(rc, permission.JsonObject(existingData), permission.ActionDelete)
	canDelete := deleteDecision.Allowed

	var transitions []TransitionAvailability
	var currentState string
	if entity.Workflow != nil {
		if raw, ok := existingData[entity.Workflow.StateField]; ok {
			_ = json.Unmarshal(raw, &currentState)
		}
	}

	if entity.Workflow != nil && currentState != "" {
		for _, transition := range entity.Workflow.Transitions {
			if transition.From != currentState {
				continue
			}
			if !transitionDecision.Allowed {
				var reason *string
				if transitionDecision.Reason != "" {
					r := transitionDecision.Reason
					reason = &r
				}
				transitions = append(transitions, TransitionAvailability{Action: transition.Action, Available: false, Reason: reason})
				continue
			}
			guardErr := workflow.RunGuard(&transition, permission.JsonObject(existingData), rc)
			var reason *string
			if guardErr != nil {
				r := guardErr.Error()
				reason = &r
			}
			transitions = append(transitions, TransitionAvailability{Action: transition.Action, Available: guardErr == nil, Reason: reason})
		}
	}

	return RecordCapabilities{
		WritableFields: writableFields,
		CanUpdate:      canUpdate,
		CanDelete:      canDelete,
		Transitions:    transitions,
	}
}

// sortFieldValue: nil when the field is genuinely unset (missing key or explicit JSON null) —
// distinct from a real empty string. Collapsing the two was the root cause of a real
// keyset-pagination data-loss bug in the Rust original.
func sortFieldValue(row RecordDto, field string) *string {
	switch field {
	case "createdAt":
		s := row.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		return &s
	case "updatedAt":
		s := row.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		return &s
	}
	raw, ok := row.Data[field]
	if !ok || isJSONNull(raw) {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return &s
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		s := formatFloat(f)
		return &s
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			s := "true"
			return &s
		}
		s := "false"
		return &s
	}
	s = string(raw)
	return &s
}
