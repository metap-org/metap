// Package dashboardroutes is the optional dashboard layout endpoints (/dashboards/*) route group,
// mounted with httpapi.BuildRouter(state, cors, dashboardroutes.Mount, ...). It lives outside package
// httpapi so a service that doesn't serve it doesn't link its dependencies.
// GET/PUT /dashboards/me (any authenticated user, their own personal layout) and GET/PUT
// /dashboards/tenant-default (requireAdmin for the write, same posture routes_admin.go takes) — the
// HTTP surface for the dashboards package. Generic across every app/entity: a layout is an opaque
// JSON blob to this package and to dashboards itself, interpreted only by the frontend's widget
// catalog.
package dashboardroutes

import (
	"encoding/json"
	"net/http"

	"github.com/metap-org/metap/internal/httpapi"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/dashboards"
	"github.com/metap-org/metap/pkg/kit/errcode"
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

func getMyDashboard(w http.ResponseWriter, r *http.Request, state *httpapi.AppState) {
	rc := httpapi.RC(r)
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpapi.WriteServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpapi.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.GetEffectiveDashboard(r.Context(), tx, tenantID, userID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpapi.WriteInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func saveMyDashboard(w http.ResponseWriter, r *http.Request, state *httpapi.AppState) {
	rc := httpapi.RC(r)
	var body saveLayoutBody
	if !httpapi.DecodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpapi.WriteServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpapi.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.UpsertPersonal(r.Context(), tx, tenantID, userID, body.Layout)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpapi.WriteInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func getTenantDefaultDashboard(w http.ResponseWriter, r *http.Request, state *httpapi.AppState) {
	rc := httpapi.RC(r)
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpapi.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.GetTenantDefault(r.Context(), tx, tenantID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpapi.WriteInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func saveTenantDefaultDashboard(w http.ResponseWriter, r *http.Request, state *httpapi.AppState) {
	rc := httpapi.RC(r)
	var body saveLayoutBody
	if !httpapi.DecodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		httpapi.WriteServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "", nil)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		httpapi.WriteRouterUnavailable(w, err)
		return
	}
	config, err := dashboards.UpsertTenantDefault(r.Context(), tx, tenantID, body.Layout, userID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		httpapi.WriteInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.WriteInternalError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"data": dashboardConfigJSON(config)})
}

func Mount(r *router.Router, state *httpapi.AppState) {
	acc := httpapi.AccessGroups(r, state)
	wrap := func(h func(http.ResponseWriter, *http.Request, *httpapi.AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	acc.Authenticated.Get("/dashboards/me", wrap(getMyDashboard))
	acc.Authenticated.Put("/dashboards/me", wrap(saveMyDashboard))
	acc.Authenticated.Get("/dashboards/tenant-default", wrap(getTenantDefaultDashboard))
	acc.Admin.Put("/dashboards/tenant-default", wrap(saveTenantDefaultDashboard))
}
