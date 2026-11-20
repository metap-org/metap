// Declared actions over REST (roadmap 116): POST /api/{entity}/{id}/actions/{action} for an action
// on one record, POST /api/{entity}/actions/{action} for one on the entity. Only actions the
// entity declares (EntityDefinition.Actions) and the service registers run; the handler reads and
// writes with the caller's own RequestContext, so permissions bound it as they bound plain CRUD.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
)

type actionBody struct {
	Input crud.JsonObject `json:"input"`
}

func invokeDeclaredAction(w http.ResponseWriter, r *http.Request, state *AppState, scope string) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity, action := chi.URLParam(r, "entity"), chi.URLParam(r, "action")
	notFound := func() {
		writeServiceError(w, http.StatusNotFound, errcode.ActionNotFound, "No action "+action+" on entity "+entity+".", nil)
	}
	if state.Actions == nil {
		notFound()
		return
	}
	def, ok := state.Metadata.Load().GetEntity(entity)
	if !ok {
		notFound()
		return
	}
	declared := false
	for _, a := range def.Actions {
		if a.Name == action && a.Scope == scope {
			declared = true
			break
		}
	}
	if !declared {
		notFound()
		return
	}
	call := crud.ActionCall{Entity: entity, Name: action, Input: crud.JsonObject{}, RC: rc}
	if scope == metadata.ActionScopeRecord {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeServiceError(w, http.StatusNotFound, errcode.RecordNotFound, "", nil)
			return
		}
		call.ID = id
	}
	if r.ContentLength != 0 {
		var body actionBody
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if body.Input != nil {
			call.Input = body.Input
		}
	}
	result, err := state.Actions.Invoke(r.Context(), call)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	if result == nil {
		result = crud.JsonObject{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func mountActions(r chi.Router, state *AppState) {
	r.Post("/api/{entity}/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		invokeDeclaredAction(w, r, state, metadata.ActionScopeEntity)
	})
	r.Post("/api/{entity}/{id}/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		invokeDeclaredAction(w, r, state, metadata.ActionScopeRecord)
	})
}
