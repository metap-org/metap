package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// mountMetadata wires the /metadata/* surface. GET /metadata/openapi.json is deliberately public
// — it only describes API shape, never tenant data. The other three require a session (requireAuth)
// but not per-entity read permission: every registered entity's shape is visible to any
// authenticated user of the tenant. They are
// what @metap/platform-ui's useEntities/useEntity/graphqlRecords and the permission matrix read, so
// a service without them renders no entity screen at all.
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
			writeServiceError(w, http.StatusNotFound, errcode.EntityNotFound, "Entity not found.", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": summary})
	})
	r.Get("/metadata/entity-access", func(w http.ResponseWriter, r *http.Request) {
		entityAccess(w, r, state)
	})
	r.Get("/metadata/actions", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAuth(w, r, state); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": knownActions()})
	})
}

// entityAccessDto is the caller's entity-level permission for one entity.
type entityAccessDto struct {
	CanRead   bool `json:"canRead"`
	CanCreate bool `json:"canCreate"`
	CanUpdate bool `json:"canUpdate"`
	CanDelete bool `json:"canDelete"`
}

// entityAccess answers GET /metadata/entity-access: {"data": {"<entity>": {canRead, canCreate,
// canUpdate, canDelete}}} for every entity this service's registry holds, with the same
// entity-level check the read and write paths make first (scope, admin bypass, policies, default
// deny). It lets a UI hide a menu entry or a button before any request would answer 403; the
// server still checks every request. A product split over several services merges the maps.
func entityAccess(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	registry, err := state.MetadataFor(r.Context(), tenantID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	out := map[string]entityAccessDto{}
	for _, entity := range registry.Definitions() {
		snapshot, err := state.Permissions.LoadSnapshot(r.Context(), tenantID, entity.Name)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		can := func(action permission.EntityAction) bool {
			return snapshot.CanPerformEntityAction(rc, entity.Name, action).Allowed
		}
		out[entity.Name] = entityAccessDto{
			CanRead:   can(permission.ActionRead),
			CanCreate: can(permission.ActionCreate),
			CanUpdate: can(permission.ActionUpdate),
			CanDelete: can(permission.ActionDelete),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
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
