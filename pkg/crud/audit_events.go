package crud

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditQuery selects one page of a record's audit history (ListAuditEvents).
type AuditQuery struct {
	// IncludeChildren also returns the history of every record whose entity declares this
	// entity as its EntityAuditConfig.Parent and points at this record — limited to the child
	// entities the caller may read, deleted children included.
	IncludeChildren bool
	Entities        []string // keep only entries of these entities
	Actions         []string // keep only these actions
	ActorUserID     *uuid.UUID
	From, To        *time.Time
	Cursor          *string // the previous page's NextCursor
	Limit           int     // default DefaultAuditPageSize, at most audit.MaxPageSize
}

// AuditPage is one page of history, newest first. NextCursor is nil on the last page.
type AuditPage struct {
	Data       []audit.Row
	NextCursor *string
}

// DefaultAuditPageSize is the page size when AuditQuery.Limit is unset.
const DefaultAuditPageSize = 50

// ListAuditEvents is the read side of recordAudit — create/update/delete/transition history for
// one record (and optionally its children), one page at a time. Record-level (ABAC) read
// permission on the record via CheckRecordPermission; each entry's diff is masked to the fields
// the caller may read on that entry's own record. Returns whatever rows exist regardless of the
// entity's *current* EntityAuditConfig.Enabled flag — toggling audit off must not make
// already-recorded history disappear.
func (s *CrudService) ListAuditEvents(ctx context.Context, entityName string, recordID uuid.UUID, q AuditQuery, rc *permission.RequestContext) (AuditPage, error) {
	if err := s.CheckRecordPermission(ctx, entityName, recordID, permission.ActionRead, rc); err != nil {
		return AuditPage{}, err
	}
	if s.auditStore == nil {
		return AuditPage{Data: []audit.Row{}}, nil
	}
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return AuditPage{}, err
	}

	query := audit.Query{
		Entity: entityName, RecordID: recordID,
		Entities: q.Entities, Actions: q.Actions, ActorUserID: q.ActorUserID, From: q.From, To: q.To,
		Limit: q.Limit,
	}
	if query.Limit <= 0 {
		query.Limit = DefaultAuditPageSize
	}
	if query.Limit > audit.MaxPageSize {
		query.Limit = audit.MaxPageSize
	}
	if q.Cursor != nil && *q.Cursor != "" {
		after, ok := decodeAuditCursor(*q.Cursor)
		if !ok {
			return AuditPage{}, NewServiceErrorMsg(http.StatusBadRequest, errcode.InvalidCursor, "`cursor` is not a cursor this endpoint issued.")
		}
		query.After = &after
	}
	if q.IncludeChildren {
		children, err := s.readableAuditChildren(ctx, tenantID, entityName, rc)
		if err != nil {
			return AuditPage{}, err
		}
		query.ChildEntities = children
	}

	events, err := s.auditStore.Query(ctx, tenantID, query)
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Data: events}
	if len(events) > query.Limit {
		page.Data = events[:query.Limit]
		last := page.Data[len(page.Data)-1]
		next := encodeAuditCursor(audit.Cursor{OccurredAt: last.OccurredAt, ID: last.ID})
		page.NextCursor = &next
	}

	// One visibility lookup per distinct record on the page, not per entry.
	visibilities := map[string]diffVisibility{}
	for i := range page.Data {
		row := &page.Data[i]
		key := row.Entity + "/" + row.RecordID.String()
		visibility, seen := visibilities[key]
		if !seen {
			visibility, err = s.auditDiffVisibility(ctx, row.Entity, row.RecordID, rc)
			if err != nil {
				return AuditPage{}, err
			}
			visibilities[key] = visibility
		}
		maskDiff(&row.Diff, visibility)
	}
	return page, nil
}

// readableAuditChildren lists the entities whose audit parent is entityName and which the caller
// may read at the entity level.
func (s *CrudService) readableAuditChildren(ctx context.Context, tenantID uuid.UUID, entityName string, rc *permission.RequestContext) ([]string, error) {
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, def := range registry.Definitions() {
		if def.AuditParentEntity() != entityName {
			continue
		}
		snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, def.Name)
		if err != nil {
			return nil, err
		}
		if snapshot.CanPerformEntityAction(rc, def.Name, permission.ActionRead).Allowed {
			out = append(out, def.Name)
		}
	}
	return out, nil
}

func encodeAuditCursor(c audit.Cursor) string { return audit.EncodeCursor(c) }

func decodeAuditCursor(raw string) (audit.Cursor, bool) { return audit.DecodeCursor(raw) }

// diffVisibility is how much of this record's audit diffs the caller may see. nil fields means
// "nothing" (record-level access itself is state-dependent); non-nil names the readable field
// set.
type diffVisibility struct {
	nothing bool
	fields  map[string]bool
}

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
