package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap/pkg/metadata"
)

// mountMetadata wires the /metadata/* surface, ported from crates/metap-http/src/routes/metadata.rs
// (recovered from git history). GET /metadata/openapi.json is deliberately public — it only
// describes API shape, never tenant data. The other three require a session (requireAuth) but not
// per-entity read permission: every registered entity's shape is visible to any authenticated
// user of the tenant (same as the Rust original; see that file's doc comment for the audit note).
// They are what @metap/platform-ui's useEntities/useEntity/graphqlRecords and the permission
// matrix read, so a service without them renders no entity screen at all.
func mountMetadata(r chi.Router, state *AppState) {
	r.Get("/metadata/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, metadata.GenerateOpenAPIDocument())
	})
	r.Get("/metadata/entities", func(w http.ResponseWriter, r *http.Request) {
		registry, ok := tenantMetadata(w, r, state)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": registry.ListEntities()})
	})
	r.Get("/metadata/entities/{entity}", func(w http.ResponseWriter, r *http.Request) {
		registry, ok := tenantMetadata(w, r, state)
		if !ok {
			return
		}
		summary, found := registry.GetEntityMetadata(chi.URLParam(r, "entity"))
		if !found {
			writeServiceError(w, http.StatusNotFound, "entity_not_found", "Entity not found.", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": summary})
	})
	r.Get("/metadata/actions", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAuth(w, r, state); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": knownActions()})
	})
}

// tenantMetadata authenticates the caller and resolves the registry their tenant sees (tenant-
// scoped low-code entities included, when a MetadataResolver is wired).
func tenantMetadata(w http.ResponseWriter, r *http.Request, state *AppState) (*metadata.MetadataRegistry, bool) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return nil, false
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return nil, false
	}
	registry, err := state.MetadataFor(r.Context(), tenantID)
	if err != nil {
		writeInternalError(w, err)
		return nil, false
	}
	return registry, true
}
