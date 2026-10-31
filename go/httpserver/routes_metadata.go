package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap-go/metadata"
)

// mountMetadata wires only the public GET /metadata/openapi.json for this core wave — the
// protected GET /metadata/entities (per-tenant registry listing) is deferred alongside the
// other admin/platform routes.
func mountMetadata(r chi.Router) {
	r.Get("/metadata/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, metadata.GenerateOpenAPIDocument())
	})
}
