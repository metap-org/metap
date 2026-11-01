// GET/PUT/DELETE /platform/config — the platform-admin surface over config's platform-writable
// tiers. Gated by requirePlatformAdmin, not requireAdmin: these keys are fleet-wide, so a
// tenant's own admin has no business reading or writing them. Since this surface also sets the
// fleet default for Tenant-tier keys — the middle link of
// declared default <- platform_configs <- tenant_configs — which each tenant may then override
// through /admin/config (routes_tenant_config.go). Operator-tier keys are never listed or
// writable from here. Ported from crates/metap-http/src/routes/platform_config.rs (recovered
// from git history, see routes_admin.go's own doc comment for why).
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap-go/config"
)

func configLevelName(level config.Level) string {
	switch level {
	case config.LevelOperator:
		return "operator"
	case config.LevelPlatformGlobal:
		return "platformGlobal"
	default:
		return "tenant"
	}
}

// configErrorResponse maps a *config.Error onto the HTTP response — shared with
// routes_tenant_config.go.
func configErrorResponse(w http.ResponseWriter, err error) {
	cfgErr, ok := err.(*config.Error)
	if !ok {
		writeInternalError(w, err)
		return
	}
	switch cfgErr.Kind {
	case config.ErrUnknownKey:
		// 404, not 400: an unknown key is an addressing mistake, and this route is keyed by path.
		writeServiceError(w, http.StatusNotFound, "unknown_config_key", "No config key named \""+cfgErr.Key+"\".", nil)
	case config.ErrNotWritable:
		// 403 rather than 400 — the value may well have been fine; the caller is not allowed to
		// set this key at all, and saying so plainly is what makes the tier boundary legible.
		writeServiceError(w, http.StatusForbidden, "config_key_not_writable",
			"Config key \""+cfgErr.Key+"\" cannot be set here: "+cfgErr.Reason, nil)
	case config.ErrInvalid:
		writeServiceError(w, http.StatusUnprocessableEntity, "invalid_config_value",
			"Invalid value for \""+cfgErr.Key+"\": "+cfgErr.Reason, nil)
	default:
		writeInternalError(w, cfgErr.Cause)
	}
}

func listPlatformConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	if _, ok := requirePlatformAdmin(w, r, state); !ok {
		return
	}
	snapshot := state.Config.Current()
	view := snapshot.PlatformWritableView()
	items := make([]map[string]any, len(view))
	for i, entry := range view {
		items[i] = map[string]any{
			"key": entry.Def.Key, "value": entry.Value, "level": configLevelName(entry.Def.Level),
			// A platform admin setting one of these is setting a default, not a decision — the
			// value a tenant sees may be its own.
			"tenantOverridable": entry.Def.Level == config.LevelTenant,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

// appliesImmediately reports whether a change takes effect without a restart (false for the
// rate-limit keys, which are baked into a middleware layer at router-build time).
func appliesImmediately(key string) bool {
	return key != config.HTTPRateLimitPerMs && key != config.HTTPRateLimitBurst
}

type setConfigBody struct {
	Value json.RawMessage `json:"value"`
}

func setPlatformConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	if _, ok := requirePlatformAdmin(w, r, state); !ok {
		return
	}
	key := chi.URLParam(r, "key")
	var body setConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	if err := state.Config.SetPlatformGlobal(r.Context(), key, body.Value); err != nil {
		configErrorResponse(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"key": key, "value": body.Value, "appliesImmediately": appliesImmediately(key)},
	})
}

// resetPlatformConfig clears an override so the key falls back to its declared default —
// distinct from setting it to the default's current value, which would pin it against a future
// change to that default.
func resetPlatformConfig(w http.ResponseWriter, r *http.Request, state *AppState) {
	if _, ok := requirePlatformAdmin(w, r, state); !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if err := state.Config.ResetPlatformGlobal(r.Context(), key); err != nil {
		configErrorResponse(w, err)
		return
	}
	value := state.Config.Current().Get(key)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"key": key, "value": value, "appliesImmediately": appliesImmediately(key)},
	})
}

func mountPlatformConfig(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/platform/config", wrap(listPlatformConfig))
	r.Put("/platform/config/{key}", wrap(setPlatformConfig))
	r.Delete("/platform/config/{key}", wrap(resetPlatformConfig))
}
