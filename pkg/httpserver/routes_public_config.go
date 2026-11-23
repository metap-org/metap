// GET /public/config — unauthenticated, resolving the tenant from the Host header, and returning only
// keys declared public in config.Registry (the tenant and platform tiers are admin.ConfigAdminService).
//
// Why it has to exist separately: branding must render on the login screen, and at that moment the
// browser has no session and the server knows nothing about who is asking. The Host header is
// attacker-controlled and is treated purely as a presentation hint — an unrecognized hostname returns
// the fleet-wide values, not a 404 (answering differently would make this a tenant-existence oracle),
// and X-Forwarded-Host is deliberately never consulted.
package httpserver

import (
	"net/http"

	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/control"
)

// requestHostname is the Host header, or ("", false) when it is absent or unusable.
// X-Forwarded-Host is deliberately not consulted — see this file's doc comment.
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

func mountPublicConfig(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	acc.Public.Get("/public/config", func(w http.ResponseWriter, r *http.Request) { publicConfig(w, r, state) })
}
