// Generic /api/{entity}/{id}/workflow-events — read-only transition history for one record,
// parameterized by :entity the same way /attachments is. Read-only, so only Read permission is
// checked, both entity-level and record-level (ABAC), via CrudService.CheckRecordPermission.
package httpserver

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

func listWorkflowEvents(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	recordID, err := uuid.Parse(r.PathValue("recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}

	cerr := state.Crud.CheckRecordPermission(r.Context(), entity, recordID, permission.ActionRead, rc)
	if cerr != nil {
		writeCrudError(w, cerr)
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

func mountWorkflowEvents(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	acc.Authenticated.Get("/api/{entity}/{recordId}/workflow-events", func(w http.ResponseWriter, r *http.Request) {
		listWorkflowEvents(w, r, state)
	})
}
