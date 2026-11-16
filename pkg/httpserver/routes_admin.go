// Admin-gated policy management over HTTP — PermissionService.ListPolicies/CreatePolicy/
// DeletePolicy/Explain, plus the context-cache invalidation. Users and roles moved to gRPC
// (grpcapi.UserAdminService, roadmap 113). Every
// handler here uses requireAdmin, not requireAuth — the admin role check happens once, so it can't
// be forgotten on a future route added to this file.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

func policyToJSON(row permission.PolicyRow) map[string]any {
	var field any
	if row.Field != "" {
		field = row.Field
	}
	var roles any
	if row.HasRoles {
		roles = row.Roles
	}
	var createdBy any
	if row.CreatedBy != nil {
		createdBy = *row.CreatedBy
	}
	return map[string]any{
		"id": row.ID, "tenantId": row.TenantID, "entity": row.Entity, "action": row.Action,
		"field": field, "subject": row.Subject, "roles": roles, "condition": row.Condition,
		"createdBy": createdBy, "effect": row.Effect.String(),
	}
}

func invalidateContextAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`userId` must be a UUID.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	state.ContextAttributesCache.Invalidate(tenantID, userID)
	w.WriteHeader(http.StatusNoContent)
}

func listPoliciesAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	entity := r.URL.Query().Get("entity")
	rows, err := state.Permissions.ListPolicies(r.Context(), tenantID, entity, entity != "")
	if err != nil {
		writeInternalError(w, err)
		return
	}
	data := make([]map[string]any, len(rows))
	for i, row := range rows {
		data[i] = policyToJSON(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type createPolicyBody struct {
	Entity    string                      `json:"entity"`
	Action    string                      `json:"action"`
	Roles     []string                    `json:"roles,omitempty"`
	Condition *permission.PolicyCondition `json:"condition,omitempty"`
	Field     *string                     `json:"field,omitempty"`
	Subject   *string                     `json:"subject,omitempty"`
	Effect    *string                     `json:"effect,omitempty"`
}

func createPolicyAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body createPolicyBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	createdBy := parseOptionalUUID(rc.UserID)
	subject := permission.SubjectContext
	if body.Subject != nil && *body.Subject == "record" {
		subject = permission.SubjectRecord
	}
	effect := permission.EffectAllow
	if body.Effect != nil {
		effect = permission.ParsePolicyEffect(*body.Effect)
	}
	field := ""
	if body.Field != nil {
		field = *body.Field
	}
	row, err := state.Permissions.CreatePolicy(r.Context(), tenantID, body.Entity, body.Action, body.Roles,
		body.Roles != nil, body.Condition, createdBy, field, &subject, effect)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": policyToJSON(row)})
}

// knownActions is derived from permission.AllEntityActions (single source of truth) rather than
// a second hand-typed list.
func knownActions() []string {
	out := make([]string, len(permission.AllEntityActions))
	for i, a := range permission.AllEntityActions {
		out[i] = a.String()
	}
	return out
}

func isKnownAction(action string) bool {
	for _, a := range knownActions() {
		if a == action {
			return true
		}
	}
	return false
}

type seedDefaultPoliciesBody struct {
	Entity  string   `json:"entity"`
	Roles   []string `json:"roles"`
	Actions []string `json:"actions,omitempty"`
}

// seedDefaultPoliciesAdmin bulk-creates one context-subject, no-condition (pure RBAC) policy per
// action for roles on entity — the ergonomic counterpart to createPolicyAdmin now that
// PermissionService denies by default when an entity/action has no policy at all.
func seedDefaultPoliciesAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body seedDefaultPoliciesBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if len(body.Roles) == 0 {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`roles` must not be empty.", nil)
		return
	}
	actions := body.Actions
	if len(actions) == 0 {
		actions = knownActions()
	}
	for _, a := range actions {
		if !isKnownAction(a) {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed,
				"Unknown action \""+a+"\" — must be one of "+joinQuoted(knownActions())+".", nil)
			return
		}
	}

	createdBy := parseOptionalUUID(rc.UserID)
	subject := permission.SubjectContext
	created := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		row, err := state.Permissions.CreatePolicy(r.Context(), tenantID, body.Entity, action, body.Roles, true,
			nil, createdBy, "", &subject, permission.EffectAllow)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		created = append(created, policyToJSON(row))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": created})
}

func deletePolicyAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`id` must be a UUID.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if err := state.Permissions.DeletePolicy(r.Context(), tenantID, id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type matrixGrant struct {
	Role   *string `json:"role"`
	Action string  `json:"action"`
}

type syncMatrixBody struct {
	Entity string        `json:"entity"`
	Grants []matrixGrant `json:"grants"`
}

// syncMatrixPoliciesAdmin is the RBAC permission matrix's single save call — replaces every
// basic-shaped policy for body.Entity with exactly body.Grants in one atomic operation, instead
// of the matrix firing one POST/DELETE per checkbox click. Never touches an Advanced-tab policy
// (condition/field/record-subject/deny).
func syncMatrixPoliciesAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body syncMatrixBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	for _, g := range body.Grants {
		if !isKnownAction(g.Action) {
			writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed,
				"Unknown action \""+g.Action+"\" — must be one of "+joinQuoted(knownActions())+".", nil)
			return
		}
	}
	createdBy := parseOptionalUUID(rc.UserID)
	grants := make([]permission.RoleActionGrant, len(body.Grants))
	for i, g := range body.Grants {
		role := ""
		if g.Role != nil {
			role = *g.Role
		}
		grants[i] = permission.RoleActionGrant{Role: role, HasRole: g.Role != nil, Action: g.Action}
	}
	rows, err := state.Permissions.SyncBasicPolicies(r.Context(), tenantID, body.Entity, grants, createdBy)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	data := make([]map[string]any, len(rows))
	for i, row := range rows {
		data[i] = policyToJSON(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type explainBody struct {
	Entity string                     `json:"entity"`
	Action string                     `json:"action"`
	Field  *string                    `json:"field,omitempty"`
	Record map[string]json.RawMessage `json:"record,omitempty"`
}

func explainPolicyAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body explainBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	field := ""
	hasField := body.Field != nil
	if hasField {
		field = *body.Field
	}
	explanation, err := state.Permissions.Explain(r.Context(), rc, body.Entity, body.Action, field, hasField,
		permission.JsonObject(body.Record), body.Record != nil)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": explanation})
}

func parseOptionalUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

func joinQuoted(items []string) string {
	out := "["
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += "\"" + s + "\""
	}
	return out + "]"
}

func mountAdmin(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Post("/admin/users/{userId}/context/invalidate", wrap(invalidateContextAdmin))
	r.Get("/admin/policies", wrap(listPoliciesAdmin))
	r.Post("/admin/policies", wrap(createPolicyAdmin))
	r.Post("/admin/policies/seed-defaults", wrap(seedDefaultPoliciesAdmin))
	r.Put("/admin/policies/matrix", wrap(syncMatrixPoliciesAdmin))
	r.Post("/admin/policies/explain", wrap(explainPolicyAdmin))
	r.Delete("/admin/policies/{id}", wrap(deletePolicyAdmin))
}
