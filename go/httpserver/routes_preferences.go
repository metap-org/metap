// Self-service GET/PUT for the caller's own peripherals preferences row — the backend half of
// i18n. A separate top-level path (/preferences, not /api/preferences) so it can't collide with
// /api/{entity}*'s wildcard. Ported from crates/metap-http/src/routes/preferences.rs (recovered
// from git history, see routes_admin.go's own doc comment for why).
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/peripherals"
)

// supportedLocales is a conservative allowlist rather than accepting any string — a typo'd
// locale would otherwise silently fall back to the frontend's default with no server-side signal
// anything was wrong.
var supportedLocales = []string{"en", "vi"}

func isSupportedLocale(locale string) bool {
	for _, l := range supportedLocales {
		if l == locale {
			return true
		}
	}
	return false
}

func getPreferences(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	locale, err := peripherals.GetLocale(r.Context(), state.Pool, tenantID, userID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"locale": locale}})
}

type updatePreferencesBody struct {
	Locale string `json:"locale"`
}

func updatePreferences(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	var body updatePreferencesBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	if !isSupportedLocale(body.Locale) {
		writeServiceError(w, http.StatusBadRequest, "validation_failed",
			"`locale` must be one of: en, vi.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if err := peripherals.SetLocale(r.Context(), state.Pool, tenantID, userID, body.Locale); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"locale": body.Locale}})
}

func mountPreferences(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/preferences", wrap(getPreferences))
	r.Put("/preferences", wrap(updatePreferences))
}
