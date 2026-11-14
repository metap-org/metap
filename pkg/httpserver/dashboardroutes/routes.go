// Package dashboardroutes is the optional dashboard layout endpoints (/dashboards/*) route group,
// mounted with httpserver.BuildRouter(state, cors, dashboardroutes.Mount, ...). It lives outside package
// httpserver so a service that doesn't serve it doesn't link its dependencies.
//
// GET/PUT /dashboards/me (any authenticated user, their own personal layout) and
// GET/PUT /dashboards/tenant-default (requireAdmin for the write, same posture routes_admin.go
// takes) — the HTTP surface for the dashboards package. Generic across every app/entity: a
// layout is an opaque JSON blob to this package and to dashboards itself, interpreted only by the
// frontend's widget catalog. Ported from crates/metap-http/src/routes/dashboards.rs (recovered
// from git history, see routes_admin.go's own doc comment for why).
package dashboardroutes

import (
	"encoding/json"
	"net/http"

	"github.com/metap-org/metap/pkg/httpserver"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/dashboards"
)

func dashboardConfigJSON(c *dashboards.DashboardConfig) any {
	if c == nil {
		return nil
	}
	var ownerUserID any
	if c.HasOwner {
		ownerUserID = c.OwnerUserID
	}
	return map[string]any{
		"id": c.ID, "ownerUserId": ownerUserID, "layout": c.Layout, "updatedAt": c.UpdatedAt,
	}
}

type saveLayoutBody struct {
	Layout json.RawMessage `json:"layout"`
}

func getMyDashboard(w http.ResponseWriter, r *http.Request, state *httpserver.AppState) {
	rc, ok := httpserver.RequireAuth(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpserver.WriteServiceError(w, http.StatusUnauthorized, "unauthorized", "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.GetEffectiveDashboard(r.Context(), tx, tenantID, userID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpserver.WriteInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func saveMyDashboard(w http.ResponseWriter, r *http.Request, state *httpserver.AppState) {
	rc, ok := httpserver.RequireAuth(w, r, state)
	if !ok {
		return
	}
	var body saveLayoutBody
	if !httpserver.DecodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpserver.WriteServiceError(w, http.StatusUnauthorized, "unauthorized", "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.UpsertPersonal(r.Context(), tx, tenantID, userID, body.Layout)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpserver.WriteInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func getTenantDefaultDashboard(w http.ResponseWriter, r *http.Request, state *httpserver.AppState) {
	rc, ok := httpserver.RequireAuth(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.GetTenantDefault(r.Context(), tx, tenantID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpserver.WriteInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func saveTenantDefaultDashboard(w http.ResponseWriter, r *http.Request, state *httpserver.AppState) {
	rc, ok := httpserver.RequireAdmin(w, r, state)
	if !ok {
		return
	}
	var body saveLayoutBody
	if !httpserver.DecodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpserver.WriteServiceError(w, http.StatusUnauthorized, "unauthorized", "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.UpsertTenantDefault(r.Context(), tx, tenantID, body.Layout, userID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpserver.WriteInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpserver.WriteInternalError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func Mount(r chi.Router, state *httpserver.AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *httpserver.AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/dashboards/me", wrap(getMyDashboard))
	r.Put("/dashboards/me", wrap(saveMyDashboard))
	r.Get("/dashboards/tenant-default", wrap(getTenantDefaultDashboard))
	r.Put("/dashboards/tenant-default", wrap(saveTenantDefaultDashboard))
}
