package httpapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// RecordActionRoute serves a record-scoped action over REST for callers that want it next to the
// gRPC ActionService: it authenticates, parses the {id} path parameter, runs the same core the
// action handler runs, and answers {"data": result} or the usual error envelope. label names the
// record in the 400 for a malformed id ("zone" -> "Invalid zone id.").
func RecordActionRoute(state *AppState, label string, run func(ctx context.Context, rc *permission.RequestContext, id uuid.UUID) (map[string]any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc, ok := RequireAuth(w, r, state)
		if !ok {
			return
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			WriteServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "Invalid "+label+" id.", nil)
			return
		}
		out, err := run(r.Context(), rc, id)
		if err != nil {
			WriteCrudError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"data": out})
	}
}
