package httpserver

import (
	"net/http"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/permission"
)

// Entity-level role gate (roadmap 120, P1c). On the generic record routes it refuses a caller whose
// roles the tenant's entity-level policy does not allow for the verb, before the handler reads a
// body, parses a query or opens a transaction.
//
// It asks the same PermissionService the record service asks, so the answer is the same; with a
// MatrixStore behind it the answer comes from memory, so a refusal costs no query. Where the
// policy leaves the decision to the record (conditions on the record, scopes, field rules) the
// entity level is open and the record service decides, as before. The record service still checks
// on its own until it is reached only through a gated backend (roadmap 120, P2).
//
// An entity this process does not know passes through, so the record service keeps answering it
// (404 entity_not_found, per-tenant metadata) exactly as it did.

// gateEntity gates a route on the entity-level policy for verb.
func gateEntity(state *AppState, verb permission.EntityAction) router.Middleware {
	return gate(state, func(r *http.Request, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error) {
		switch verb {
		case permission.ActionRead:
			return state.Permissions.CanReadEntity(r.Context(), rc, entity)
		case permission.ActionCreate:
			return state.Permissions.CanCreateEntity(r.Context(), rc, entity)
		case permission.ActionUpdate:
			return state.Permissions.CanUpdateEntity(r.Context(), rc, entity)
		case permission.ActionDelete:
			return state.Permissions.CanDeleteEntity(r.Context(), rc, entity)
		}
		return permission.PermissionDecision{}, errUnknownVerb
	})
}

// gateTransition gates POST /api/{entity}/{id}/transitions/{action} on the policy for that action.
func gateTransition(state *AppState) router.Middleware {
	return gate(state, func(r *http.Request, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error) {
		return state.Permissions.CanTransitionAction(r.Context(), rc, entity, r.PathValue("action"))
	})
}

type gateError string

func (e gateError) Error() string { return string(e) }

const errUnknownVerb = gateError("gate: unknown verb")

func gate(state *AppState, decide func(*http.Request, *permission.RequestContext, string) (permission.PermissionDecision, error)) router.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := RC(r)
			entity := r.PathValue("entity")
			if rc == nil || !knownEntity(state, entity) {
				next.ServeHTTP(w, r)
				return
			}
			d, err := decide(r, rc, entity)
			if err != nil {
				writeInternalError(w, err)
				return
			}
			if !d.Allowed {
				reason := d.Reason
				if reason == "" {
					reason = errcode.Forbidden
				}
				writeCrudError(w, crud.NewServiceError(http.StatusForbidden, reason))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func knownEntity(state *AppState, entity string) bool {
	reg := state.Metadata.Load()
	if reg == nil {
		return false
	}
	_, ok := reg.GetEntity(entity)
	return ok
}
