// Generic /api/{entity}/{id}/audit-events — read-only create/update/delete/transition history for
// one record. Distinct from /workflow-events (state-machine transitions only) — this is
// metap-audit's general business audit trail, opt-in per entity via EntityDefinition.Audit.
// Read-only, record-level ABAC via CrudService.ListAuditEvents (which itself calls
// CheckRecordPermission internally).
package httpserver

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func listRecordAuditEvents(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := r.PathValue("entity")
	recordID, err := uuid.Parse(r.PathValue("recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}

	q, ok := auditQueryFrom(w, r)
	if !ok {
		return
	}
	result, err := state.Crud.ListAuditEvents(r.Context(), entity, recordID, q, rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": result.Data,
		"page": map[string]any{"nextCursor": result.NextCursor},
	})
}

// auditQueryFrom reads the page/filter query parameters:
//
//	includeChildren=true   also the history of records whose audit parent is this record
//	entities=a,b           only entries of these entities
//	actions=create,update  only these actions
//	actor=<uuid>           only this actor
//	from=, to=             RFC 3339 instants, occurredAt >= from and < to
//	cursor=, limit=        keyset paging (the previous page's page.nextCursor)
func auditQueryFrom(w http.ResponseWriter, r *http.Request) (crud.AuditQuery, bool) {
	values := r.URL.Query()
	q := crud.AuditQuery{IncludeChildren: values.Get("includeChildren") == "true"}
	split := func(raw string) []string {
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	q.Entities = split(values.Get("entities"))
	q.Actions = split(values.Get("actions"))
	if raw := values.Get("actor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`actor` must be a user id.", nil)
			return q, false
		}
		q.ActorUserID = &id
	}
	for _, bound := range []struct {
		name string
		dst  **time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := values.Get(bound.name); raw != "" {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`"+bound.name+"` must be an RFC 3339 time.", nil)
				return q, false
			}
			*bound.dst = &at
		}
	}
	if raw := values.Get("cursor"); raw != "" {
		q.Cursor = &raw
	}
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`limit` must be a positive integer.", nil)
			return q, false
		}
		q.Limit = n
	}
	return q, true
}

func mountAuditEvents(r *router.Router, state *AppState) {
	r.Get("/api/{entity}/{recordId}/audit-events", func(w http.ResponseWriter, r *http.Request) {
		listRecordAuditEvents(w, r, state)
	})
}
