// Generic /api/{entity}/{id}/audit-events — read-only create/update/delete/transition history
// for one record. Distinct from /workflow-events (state-machine transitions only) — this is
// metap-audit's general business audit trail, opt-in per entity via EntityDefinition.Audit.
// Read-only, record-level ABAC via CrudService.ListAuditEvents (which itself calls
// CheckRecordPermission internally). Ported from
// crates/metap-http/src/routes/audit_events.rs.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func listRecordAuditEvents(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	recordID, err := uuid.Parse(chi.URLParam(r, "recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}

	result, err := state.Crud.ListAuditEvents(r.Context(), entity, recordID, rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data})
}

func mountAuditEvents(r chi.Router, state *AppState) {
	r.Get("/api/{entity}/{recordId}/audit-events", func(w http.ResponseWriter, r *http.Request) {
		listRecordAuditEvents(w, r, state)
	})
}
