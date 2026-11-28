// REST entity CRUD — /api/{entity}*.
// This handler does not invalidate the auth-context cache. CrudService.Update would, once
// WithContextInvalidation is wired, which nothing does yet: see "Known gaps" in CLAUDE.md.
package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// reservedQueryKeys are list parameters, never field filters. "ids", "expand" and "fields" are
// roadmap 116's: ids and fields are served here, expand by the API gateway (pkg/apigateway).
var reservedQueryKeys = map[string]bool{"limit": true, "sort": true, "cursor": true, "listView": true, "jql": true, "offset": true, "search": true, "withTotal": true,
	"ids": true, "expand": true, "fields": true}

// maxIDsPerRequest bounds GET /api/{entity}?ids=.
const maxIDsPerRequest = 200

func parseListInput(w http.ResponseWriter, values map[string][]string) (query.ListInput, bool) {
	limit := int64(30)
	if raw := firstValue(values, "limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 || n > 200 {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`limit` must be a positive integer no greater than 200.", nil)
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
	if v := firstValue(values, "search"); v != "" {
		input.Search = &v
	}
	if raw := firstValue(values, "offset"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeServiceError(w, http.StatusBadRequest, errcode.InvalidOffset, "`offset` must be a non-negative integer.", nil)
			return query.ListInput{}, false
		}
		input.Offset = n
	}
	input.WithTotal = firstValue(values, "withTotal") == "true"
	return input, true
}

// listRecordsByID is GET /api/{entity}?ids=a,b,c: the records with those ids that exist and the
// caller may read, in one call (what an `expand` across services batches into). Unknown, deleted
// or denied ids are simply absent.
func listRecordsByID(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext, entity, raw string) {
	fields, ok := parseFields(w, r, state, rc, entity, r.URL.Query())
	if !ok {
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxIDsPerRequest {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed,
			"`ids` may list at most "+strconv.Itoa(maxIDsPerRequest)+" ids.", nil)
		return
	}
	ids := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`ids` must be comma-separated record ids.",
				map[string][]string{"ids": {"not a record id: " + p}})
			return
		}
		ids = append(ids, id)
	}
	results, err := state.RecordBackend().GetMany(r.Context(), entity, ids, rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	records := make([]crud.RecordDto, 0, len(results))
	for _, res := range results {
		records = append(records, res.Record)
	}
	projectRecords(records, fields)
	writeJSON(w, http.StatusOK, map[string]any{"data": records, "page": map[string]any{"limit": len(ids)}})
}

func firstValue(values map[string][]string, key string) string {
	if vs, ok := values[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func listRecords(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	if raw := r.URL.Query().Get("ids"); raw != "" {
		listRecordsByID(w, r, state, rc, entity, raw)
		return
	}
	fields, ok := parseFields(w, r, state, rc, entity, r.URL.Query())
	if !ok {
		return
	}
	input, ok := parseListInput(w, r.URL.Query())
	if !ok {
		return
	}

	result, err := state.RecordBackend().List(r.Context(), entity, input, rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	projectRecords(result.Data, fields)
	page := map[string]any{"limit": result.Page.Limit, "nextCursor": result.Page.NextCursor, "offset": result.Page.Offset}
	if result.Page.Total != nil {
		page["total"] = *result.Page.Total
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "page": page, "capabilities": result.Capabilities})
}

func getRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}

	fields, ok := parseFields(w, r, state, rc, entity, r.URL.Query())
	if !ok {
		return
	}
	result, err := state.RecordBackend().Get(r.Context(), entity, id, rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	result.Record.Data = projectData(result.Record.Data, fields) // get carries no RelatedDisplay
	// The record plus a "capabilities" key merged in at the top level.
	recordJSON, err := json.Marshal(result.Record)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(recordJSON, &data); err != nil {
		writeInternalError(w, err)
		return
	}
	capsJSON, _ := json.Marshal(result.Capabilities)
	data["capabilities"] = capsJSON
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type recordBody struct {
	Data   crud.JsonObject `json:"data"`
	Reason *string         `json:"reason,omitempty"`
}

func createRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	var body recordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}

	result, err := state.RecordBackend().Create(r.Context(), entity, body.Data, rc, body.Reason)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": result})
}

type updateBody struct {
	Version int32           `json:"version"`
	Data    crud.JsonObject `json:"data"`
	Reason  *string         `json:"reason,omitempty"`
}

func updateRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}
	var body updateBody
	if !decodeJSONBody(w, r, &body) {
		return
	}

	result, err := state.RecordBackend().Update(r.Context(), entity, id, body.Version, body.Data, rc, body.Reason)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

type deleteBody struct {
	Version int32   `json:"version"`
	Reason  *string `json:"reason,omitempty"`
}

func deleteRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}
	var body deleteBody
	if !decodeJSONBody(w, r, &body) {
		return
	}

	result, err := state.RecordBackend().Delete(r.Context(), entity, id, body.Version, rc, body.Reason)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

type transitionBody struct {
	Version int32           `json:"version"`
	Data    crud.JsonObject `json:"data,omitempty"`
	Reason  *string         `json:"reason,omitempty"`
}

func transitionRecord(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
		return
	}
	action := r.PathValue("action")
	var body transitionBody
	if !decodeJSONBody(w, r, &body) {
		return
	}

	result, err := state.RecordBackend().Transition(r.Context(), entity, id, action, body.Version, body.Data, rc, body.Reason)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// aggregateRecords: POST /api/{entity}/aggregate's body is query.AggregateSpec directly — the
// same raw wire shape every other transport would parse too. A POST rather than query params
// because the metric/filter set is a structured object, not a flat string map.
func aggregateRecords(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	entity := r.PathValue("entity")
	var spec query.AggregateSpec
	if err := httpx.DecodeJSON(w, r, &spec, 0); err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			writeServiceError(w, http.StatusRequestEntityTooLarge, errcode.PayloadTooLarge, "Request body is too large.", nil)
			return
		}
		writeServiceError(w, http.StatusBadRequest, errcode.InvalidAggregate, "Invalid request body.", nil)
		return
	}

	result, err := state.RecordBackend().Aggregate(r.Context(), entity, spec, rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func mountRecords(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	// Each route is gated on the entity-level policy for its verb before its handler runs.
	read, create := gateEntity(state, permission.ActionRead), gateEntity(state, permission.ActionCreate)
	update, del := gateEntity(state, permission.ActionUpdate), gateEntity(state, permission.ActionDelete)
	acc.Authenticated.Handle("GET /api/{entity}", read(wrap(listRecords)))
	acc.Authenticated.Handle("POST /api/{entity}", create(wrap(createRecord)))
	acc.Authenticated.Handle("POST /api/{entity}/aggregate", read(wrap(aggregateRecords)))
	acc.Authenticated.Handle("GET /api/{entity}/{id}", read(wrap(getRecord)))
	acc.Authenticated.Handle("PATCH /api/{entity}/{id}", update(wrap(updateRecord)))
	acc.Authenticated.Handle("DELETE /api/{entity}/{id}", del(wrap(deleteRecord)))
	acc.Authenticated.Handle("POST /api/{entity}/{id}/transitions/{action}", gateTransition(state)(wrap(transitionRecord)))
}
