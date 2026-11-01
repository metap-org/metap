// Generic /api/{entity}/{id}/workflow-events — read-only transition history for one record,
// parameterized by :entity the same way /attachments is. Read-only, so only Read permission is
// checked, both entity-level and record-level (ABAC), via CrudService.CheckRecordPermission.
// Ported from crates/metap-http/src/routes/workflow_events.rs.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/workflow"
)

func listWorkflowEvents(w http.ResponseWriter, r *http.Request, state *AppState) {
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

	checkResult, cerr := state.Crud.CheckRecordPermission(r.Context(), entity, recordID, permission.ActionRead, rc)
	if cerr != nil {
		writeInternalError(w, cerr)
		return
	}
	if !checkResult.IsOk {
		writeCrudServiceError(w, checkResult.Status, checkResult.ErrorCode, checkResult.Message, checkResult.FieldErrors)
		return
	}

	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	events, err := workflow.ListEvents(r.Context(), tx, tenantID, entity, recordID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck
	writeJSON(w, http.StatusOK, map[string]any{"data": events})
}

func mountWorkflowEvents(r chi.Router, state *AppState) {
	r.Get("/api/{entity}/{recordId}/workflow-events", func(w http.ResponseWriter, r *http.Request) {
		listWorkflowEvents(w, r, state)
	})
}
