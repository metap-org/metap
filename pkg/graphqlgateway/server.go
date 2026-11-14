// HTTP surface: POST /graphql (the only real endpoint) and GET /health.
//
// Auth here is decode-only, same as the Rust gateway: a caller needs a token that verifies
// against the TokenVerifier it was built with (JWKS or a static key) to reach any resolver at all. The SAME raw token is then forwarded verbatim to
// whichever upstream a resolver calls (gRPC metadata or an HTTP Authorization header) — that
// upstream re-verifies and does the real, tenant/role-aware authorization. Decoding locally
// first is defense in depth and a cleaner error for an obviously-bad token; it is not the
// authorization check.
package graphqlgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"io"
	"net/http"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/gqlerrors"

	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/runtime"
)

type Server struct {
	schema   graphql.Schema
	verifier *jwks.TokenVerifier
	limits   Limits

	// metadataEntities: entities whose /metadata/entities/{entity} this gateway serves itself,
	// because no service owns them as stored entities (the Analytics read models).
	registry         *metadata.MetadataRegistry
	metadataEntities map[string]bool
}

// ServeMetadataFor makes the gateway answer GET /metadata/entities/{entity} for these entities,
// from registry — the portal reads an entity's shape from there before rendering its screens.
func (s *Server) ServeMetadataFor(registry *metadata.MetadataRegistry, entities ...string) {
	s.registry = registry
	s.metadataEntities = map[string]bool{}
	for _, e := range entities {
		s.metadataEntities[e] = true
	}
}

func (s *Server) metadataAuth(w http.ResponseWriter, r *http.Request) bool {
	token, msg := requestToken(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": msg}})
		return false
	}
	if _, err := jwks.DecodeWithVerifier(r.Context(), token, s.verifier, nil); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "invalid or expired token"}})
		return false
	}
	return true
}

// handleMetadataList is GET /metadata/entities, limited to the entities this gateway serves
// itself — the portal merges it with each service's own list.
func (s *Server) handleMetadataList(w http.ResponseWriter, r *http.Request) {
	if !s.metadataAuth(w, r) {
		return
	}
	out := []metadata.EntitySummary{}
	if s.registry != nil {
		for _, e := range s.registry.ListEntities() {
			if s.metadataEntities[e.Name] {
				out = append(out, e)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.metadataAuth(w, r) {
		return
	}
	entity := r.PathValue("entity")
	summary, found := metadata.EntitySummary{}, false
	if s.registry != nil && s.metadataEntities[entity] {
		summary, found = s.registry.GetEntityMetadata(entity)
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "entity_not_found", "message": "Entity not found."}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": summary})
}

func NewServer(schema graphql.Schema, verifier *jwks.TokenVerifier, limits Limits) *Server {
	return &Server{schema: schema, verifier: verifier, limits: limits}
}

type graphqlRequestBody struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

var writeJSON = httpx.WriteJSON

func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}

	token, msg := requestToken(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"errors": []map[string]string{{"message": msg}},
		})
		return
	}
	if _, err := jwks.DecodeWithVerifier(r.Context(), token, s.verifier, nil); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"errors": []map[string]string{{"message": "invalid or expired token"}},
		})
		return
	}

	malformed := func() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"errors": []map[string]string{{"message": "malformed request body"}},
		})
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
				"errors": []map[string]string{{"message": "request body is too large"}},
			})
			return
		}
		malformed()
		return
	}

	rc := &permission.RequestContext{ForwardedBearerToken: token}
	ctx := withRequestContext(r.Context(), rc)
	execute := func(body graphqlRequestBody) *graphql.Result {
		if err := checkLimits(body.Query, body.OperationName, s.limits); err != nil {
			return &graphql.Result{Errors: []gqlerrors.FormattedError{gqlerrors.NewFormattedError(err.Error())}}
		}
		return graphql.Do(graphql.Params{
			Schema:         s.schema,
			RequestString:  body.Query,
			VariableValues: body.Variables,
			OperationName:  body.OperationName,
			Context:        ctx,
		})
	}

	// A JSON array is a batch (@metap/platform-ui's graphqlClient.ts coalesces every query issued
	// in one microtask into one request, the same GraphQLBatchRequest shape the Rust gateway
	// accepted): run each in order, answer with an array of results in the same order.
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []graphqlRequestBody
		if err := json.Unmarshal(trimmed, &batch); err != nil || len(batch) == 0 {
			malformed()
			return
		}
		if len(batch) > maxBatchSize {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"errors": []map[string]string{{"message": fmt.Sprintf("batch exceeds %d operations", maxBatchSize)}},
			})
			return
		}
		results := make([]*graphql.Result, len(batch))
		for i, body := range batch {
			results[i] = execute(body)
		}
		writeJSON(w, http.StatusOK, results)
		return
	}

	var body graphqlRequestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		malformed()
		return
	}
	writeJSON(w, http.StatusOK, execute(body))
}

const (
	maxRequestBytes = 1 << 20 // 1 MiB — every portal request is a small query + variables
	maxBatchSize    = 50
)

// requestToken mirrors metap's httpserver authenticate: an explicit Authorization header always
// wins; otherwise the HttpOnly session cookie (what the Customer Portal's browser sends — it has
// carried no Authorization header since the cookie-session move), double-submit CSRF-checked for
// any state-changing method. /graphql is POST-only, so a cookie caller always needs the CSRF
// header. Returns "" plus an error message when neither yields a usable token.
func requestToken(r *http.Request) (string, string) {
	if header := r.Header.Get("Authorization"); header != "" {
		token, ok := runtime.ParseBearer(header)
		if !ok {
			return "", "missing or malformed Authorization header"
		}
		return token, ""
	}
	session, err := r.Cookie(runtime.SessionCookieName)
	if err != nil || session.Value == "" {
		return "", "missing or malformed Authorization header"
	}
	if runtime.RequiresCSRFCheck(r.Method) {
		csrfValue, csrfOK := "", false
		if c, cErr := r.Cookie(runtime.CSRFCookieName); cErr == nil {
			csrfValue, csrfOK = c.Value, true
		}
		csrfHeader := r.Header.Get(runtime.CSRFHeaderName)
		if !runtime.CSRFMatches(csrfValue, csrfHeader, csrfOK, csrfHeader != "") {
			return "", "missing or invalid CSRF token"
		}
	}
	return session.Value, ""
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func (s *Server) Router() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", s.handleGraphQL)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("GET /metadata/entities", s.handleMetadataList)
	mux.HandleFunc("GET /metadata/entities/{entity}", s.handleMetadata)
	return mux
}
