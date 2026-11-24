// Declared actions over REST (roadmap 116): POST /api/{entity}/{id}/actions/{action} for an action
// on one record, POST /api/{entity}/actions/{action} for one on the entity. Only actions the
// entity declares (EntityDefinition.Actions) and the service registers run; the handler reads and
// writes with the caller's own RequestContext, so permissions bound it as they bound plain CRUD.
package httpapi

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

type actionBody struct {
	Input crud.JsonObject `json:"input"`
}

func invokeDeclaredAction(w http.ResponseWriter, r *http.Request, state *AppState, scope string) {
	rc := RC(r)
	entity, action := r.PathValue("entity"), r.PathValue("action")
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
	var declared *metadata.EntityAction
	for i, a := range def.Actions {
		if a.Name == action && a.Scope == scope {
			declared = &def.Actions[i]
			break
		}
	}
	if declared == nil {
		notFound()
		return
	}
	// Who may run it is decided before anything about the request is read: the tenant's policies on
	// the action, else the roles the action declares.
	if d, err := state.Permissions.CanRunAction(r.Context(), rc, entity, action, declared.Roles); err != nil {
		writeInternalError(w, err)
		return
	} else if !d.Allowed {
		writeServiceError(w, http.StatusForbidden, errcode.Forbidden, "You may not run this action.", nil)
		return
	}
	if !actionRequirementMet(w, r, state, rc, entity, declared.Requires) {
		return
	}
	call := crud.ActionCall{Entity: entity, Name: action, Input: crud.JsonObject{}, RC: rc}
	if scope == metadata.ActionScopeRecord {
		id, err := uuid.Parse(r.PathValue("id"))
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

func mountActions(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	// POST /api/{entity}/{first}/{second} is two things a ServeMux cannot hold as two patterns
	// (they overlap whenever {first} could be the word "actions"): an action on the entity
	// (first == "actions") and a sub-resource of a record that a route module registered with
	// AppState.HandleRecordPost (first is the record id, second the sub-resource: "attachments").
	acc.Authenticated.Post("/api/{entity}/{first}/{second}", func(w http.ResponseWriter, req *http.Request) {
		first, second := req.PathValue("first"), req.PathValue("second")
		if first == "actions" {
			req.SetPathValue("action", second)
			invokeDeclaredAction(w, req, state, metadata.ActionScopeEntity)
			return
		}
		if h := state.recordPosts[second]; h != nil {
			req.SetPathValue("recordId", first)
			h(w, req)
			return
		}
		writeServiceError(w, http.StatusNotFound, errcode.NotFound, "No such route.", nil)
	})
	acc.Authenticated.Post("/api/{entity}/{id}/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		invokeDeclaredAction(w, r, state, metadata.ActionScopeRecord)
	})
}

// actionRequirementMet enforces EntityAction.Requires: the entity-level permission the action's
// declaration names, checked before the handler (and so before anything it does outside records).
func actionRequirementMet(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext, entity, requires string) bool {
	if requires == "" {
		return true
	}
	var d permission.PermissionDecision
	var err error
	switch requires {
	case permission.PolicyActionRead:
		d, err = state.Permissions.CanReadEntity(r.Context(), rc, entity)
	case permission.PolicyActionCreate:
		d, err = state.Permissions.CanCreateEntity(r.Context(), rc, entity)
	case permission.PolicyActionUpdate:
		d, err = state.Permissions.CanUpdateEntity(r.Context(), rc, entity)
	case permission.PolicyActionDelete:
		d, err = state.Permissions.CanDeleteEntity(r.Context(), rc, entity)
	case permission.PolicyActionTransition:
		d, err = state.Permissions.CanTransitionEntity(r.Context(), rc, entity)
	default:
		err = fmt.Errorf("action requires unknown permission %q", requires)
	}
	if err != nil {
		writeInternalError(w, err)
		return false
	}
	if !d.Allowed {
		writeServiceError(w, http.StatusForbidden, errcode.Forbidden, "You may not run this action.", nil)
		return false
	}
	return true
}
