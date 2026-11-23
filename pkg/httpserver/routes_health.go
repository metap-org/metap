package httpserver

import (
	"net/http"

	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

func mountHealth(r *router.Router) {
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(standard.HeaderContentType, standard.MediaTypeJSON)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// writeJSON is the shared kit helper; kept as a local name for the route handlers.
var writeJSON = httpx.WriteJSON
