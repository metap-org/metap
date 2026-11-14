package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

func mountHealth(r chi.Router) {
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// writeJSON is the shared kit helper; kept as a local name for the route handlers.
var writeJSON = httpx.WriteJSON
