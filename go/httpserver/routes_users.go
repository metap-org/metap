// GET /users — lightweight {id, email} list of every user in the caller's tenant, the "pick a
// user" primitive an assignee/reporter picker needs. Deliberately not under /admin/* (requireAuth,
// not requireAdmin) — assigning an issue to a colleague isn't an admin action, unlike granting a
// role (GET /admin/users, routes_admin.go, a different shape for a different purpose). Ported
// from crates/metap-http/src/routes/users.rs (recovered from git history, see routes_admin.go's
// own doc comment for why).
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap-go/peripherals"
)

func listUsersForTenant(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
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
	users, err := peripherals.ListTenantUsers(r.Context(), tx, tenantID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck

	data := make([]map[string]any, len(users))
	for i, u := range users {
		data[i] = map[string]any{"id": u.ID, "email": u.Email}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func mountUsers(r chi.Router, state *AppState) {
	r.Get("/users", func(w http.ResponseWriter, r *http.Request) { listUsersForTenant(w, r, state) })
}
