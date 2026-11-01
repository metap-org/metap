// The two non-platform config surfaces:
//
//   - GET/PUT/DELETE /admin/config — a tenant admin reading and setting its own Tenant-tier
//     overrides. The tenant comes from the caller's verified token and from nowhere else, which
//     is the whole of the cross-tenant isolation story here: there is no request shape that
//     names a tenant, so there is none to get wrong.
//   - GET /public/config — unauthenticated, resolving the tenant from the Host header, and
//     returning only keys declared public in config.Registry.
//
// Why the public one has to exist separately: branding must render on the login screen, and at
// that moment the browser has no session and the server knows nothing about who is asking. The
// Host header is attacker-controlled and is treated purely as a presentation hint — an
// unrecognized hostname returns the fleet-wide values, not a 404 (answering differently would
// make this a tenant-existence oracle), and X-Forwarded-Host is deliberately never consulted.
//
// Ported from crates/metap-http/src/routes/tenant_config.rs (recovered from git history, see
// routes_admin.go's own doc comment for why). Deliberate simplification vs. that Rust original:
// the hostname->tenant lookup there is cached (moka, 30s TTL); this port queries
// control.tenant_hostnames directly on every call, since /public/config's own caller (a login
// page) is low-frequency enough that the extra round trip isn't worth the added cache-invalidation
// surface for this wave — a later pass can add a cache if this proves to matter.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap-go/config"
	"github.com/metap-org/metap-go/control"
)

// listTenantConfig is requireAuth, not requireAdmin: every one of these keys shapes what the app
// looks like or how long a session lasts for the user reading it, so an ordinary member seeing
// the effective values is expected. Writing is admin-gated below.
func listTenantConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	effective := state.EffectiveConfig(r.Context(), tenantID)
	view := effective.TenantView()
	items := make([]map[string]any, len(view))
	for i, entry := range view {
		items[i] = map[string]any{
			"key": entry.Def.Key, "value": entry.Value, "level": configLevelName(entry.Def.Level),
			// Distinguishes "we chose this" from "the platform chose this for us" — without it a
			// tenant admin cannot tell whether clearing the key would change anything.
			"overridden": entry.Overridden, "public": entry.Def.Public,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func setTenantConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	var body setConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
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
	if err := state.Config.SetTenant(r.Context(), tx, tenantID, key, body.Value); err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		configErrorResponse(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"key": key, "value": body.Value, "overridden": true},
	})
}

// resetTenantConfig clears this tenant's override. The key then reads back the fleet default a
// platform admin set, or the value declared in Go if there is none — never null.
func resetTenantConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
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
	if err := state.Config.ResetTenant(r.Context(), tx, tenantID, key); err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		configErrorResponse(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	value := state.EffectiveConfig(r.Context(), tenantID).Get(key)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"key": key, "value": value, "overridden": false},
	})
}

// requestHostname is the Host header, or ("", false) when it is absent or unusable.
// X-Forwarded-Host is deliberately not consulted — see this file's own doc comment.
func requestHostname(r *http.Request) (string, bool) {
	raw := r.Host
	if raw == "" {
		return "", false
	}
	return control.NormalizeHostname(raw)
}

func publicConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	var effective *config.EffectiveConfig
	if host, ok := requestHostname(r); ok {
		tenantID, found, err := control.TenantIDForHostname(r.Context(), state.Pool, host)
		if err == nil && found {
			effective = state.EffectiveConfig(r.Context(), tenantID)
		}
	}
	// No tenant (unknown hostname, no usable Host header, or a lookup failure) is not an error —
	// it reads back the fleet-wide values, which is also what a tenant that has set nothing
	// would see. A login page that would not render because a branding lookup failed is a far
	// worse outcome than an unbranded one.
	if effective == nil {
		effective = state.Config.Effective(nil)
	}

	view := effective.PublicView()
	items := make([]map[string]any, len(view))
	for i, entry := range view {
		items[i] = map[string]any{"key": entry.Key, "value": entry.Value}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func mountTenantConfig(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/admin/config", wrap(listTenantConfig))
	r.Put("/admin/config/{key}", wrap(setTenantConfig))
	r.Delete("/admin/config/{key}", wrap(resetTenantConfig))
	r.Get("/public/config", wrap(publicConfig))
}
