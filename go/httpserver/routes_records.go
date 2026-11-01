// REST entity CRUD — /api/{entity}*. Recovered from the Rust metap-http crate's git history (the
// commit immediately before "Remove REST entity CRUD, GraphQL-only entity access", #31) and
// ported essentially unchanged, per the user's explicit direction to reinstate REST for entity
// CRUD in this Go port rather than following the Rust original's later GraphQL-only decision.
//
// One behavioral note vs. that recovered file: it called invalidate_context_cache_if_auth_
// context_entity itself after a successful update. This Go port's crud.CrudService.Update
// already does that internally (via CrudService.WithContextInvalidation, ported from the fixed-
// at-the-root version of the Rust CrudService that shipped *after* the file this was recovered
// from) — so this handler does not duplicate it; see crud/crud_service.go's own doc comment.
package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/query"
)

var reservedQueryKeys = map[string]bool{"limit": true, "sort": true, "cursor": true, "listView": true, "jql": true}

func parseListInput(w http.ResponseWriter, values map[string][]string) (query.ListInput, bool) {
	limit := int64(30)
	if raw := firstValue(values, "limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 || n > 200 {
			writeServiceError(w, http.StatusBadRequest, "validation_failed", "`limit` must be a positive integer no greater than 200.", nil)
			return query.ListInput{}, false
		}
		limit = n
	}

	var filters [][2]string
	for k, vs := range values {
		if reservedQueryKeys[k] || len(vs) == 0 {
			continue
		}
		filters = append(filters, [2]string{k, vs[0]})
	}

	input := query.ListInput{Limit: limit, Filters: filters}
	if v := firstValue(values, "sort"); v != "" {
		input.Sort = &v
	}
	if v := firstValue(values, "cursor"); v != "" {
		input.Cursor = &v
	}
	if v := firstValue(values, "listView"); v != "" {
		input.ListView = &v
	}
	if v := firstValue(values, "jql"); v != "" {
		input.JQL = &v
	}
	return input, true
}

func firstValue(values map[string][]string, key string) string {
	if vs, ok := values[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func listRecords(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	input, ok := parseListInput(w, r.URL.Query())
	if !ok {
		return
	}

	result, err := state.Crud.List(r.Context(), entity, input, rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	var page any
	if result.Page != nil {
		page = map[string]any{"limit": result.Page.Limit, "nextCursor": result.Page.NextCursor}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "page": page})
}

func getRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}

	result, err := state.Crud.Get(r.Context(), entity, id, rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}

	// The record plus a "capabilities" key merged in at the top level, matching the Rust
	// handler's own serde_json::Value splice.
	recordJSON, err := json.Marshal(result.Data.Record)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(recordJSON, &data); err != nil {
		writeInternalError(w, err)
		return
	}
	capsJSON, _ := json.Marshal(result.Data.Capabilities)
	data["capabilities"] = capsJSON
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type recordBody struct {
	Data   crud.JsonObject `json:"data"`
	Reason *string         `json:"reason,omitempty"`
}

func createRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	var body recordBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}

	result, err := state.Crud.Create(r.Context(), entity, body.Data, rc, body.Reason)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": result.Data})
}

type updateBody struct {
	Version int32           `json:"version"`
	Data    crud.JsonObject `json:"data"`
	Reason  *string         `json:"reason,omitempty"`
}

func updateRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	var body updateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}

	result, err := state.Crud.Update(r.Context(), entity, id, body.Version, body.Data, rc, body.Reason)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data})
}

type deleteBody struct {
	Version int32   `json:"version"`
	Reason  *string `json:"reason,omitempty"`
}

func deleteRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	var body deleteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}

	result, err := state.Crud.Delete(r.Context(), entity, id, body.Version, rc, body.Reason)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data})
}

type transitionBody struct {
	Version int32           `json:"version"`
	Data    crud.JsonObject `json:"data,omitempty"`
	Reason  *string         `json:"reason,omitempty"`
}

func transitionRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	action := chi.URLParam(r, "action")
	var body transitionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}

	result, err := state.Crud.Transition(r.Context(), entity, id, action, body.Version, body.Data, rc, body.Reason)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data})
}

// aggregateRecords: POST /api/{entity}/aggregate's body is query.AggregateSpec directly — the
// same raw wire shape every other transport would parse too. A POST rather than query params
// because the metric/filter set is a structured object, not a flat string map.
func aggregateRecords(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	var spec query.AggregateSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_aggregate", "Invalid request body.", nil)
		return
	}

	result, err := state.Crud.Aggregate(r.Context(), entity, spec, rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data})
}

// mountRecords registers /api/{entity}* exactly as the recovered Rust router did — the
// aggregate/{entity}/aggregate route ahead of /{entity}/{id} in source order for readability
// only; chi, like axum, matches a literal path segment ahead of a {param} one regardless of
// registration order, so "aggregate" can never be swallowed as an {id} (it would fail uuid.Parse
// anyway, which is what makes this collision safe).
func mountRecords(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/api/{entity}", wrap(listRecords))
	r.Post("/api/{entity}", wrap(createRecord))
	r.Post("/api/{entity}/aggregate", wrap(aggregateRecords))
	r.Get("/api/{entity}/{id}", wrap(getRecord))
	r.Patch("/api/{entity}/{id}", wrap(updateRecord))
	r.Delete("/api/{entity}/{id}", wrap(deleteRecord))
	r.Post("/api/{entity}/{id}/transitions/{action}", wrap(transitionRecord))
}
