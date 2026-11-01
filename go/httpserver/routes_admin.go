// Admin-gated role/policy management over HTTP — the HTTP surface for peripherals.AssignRole/
// RevokeRole/ListUsers and PermissionService.ListPolicies/CreatePolicy/DeletePolicy/Explain.
// Every handler here uses requireAdmin, not requireAuth — the admin role check happens once, so
// it can't be forgotten on a future route added to this file. Ported from
// crates/metap-http/src/routes/admin.rs (recovered from git history: the commit immediately
// before "Migrate remaining REST backlog ... to GraphQL", which deleted this file along with
// cron.rs/dashboards.rs/preferences.rs/platform_config.rs/tenant_config.rs/users.rs — reinstated
// here per the user's explicit direction to keep REST rather than following that later decision).
package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
)

type createUserBody struct {
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Roles    []string `json:"roles,omitempty"`
}

// createUserAdmin provisions a new local-login user — the admin-driven counterpart to a dev
// seeding CLI, both ultimately going through peripherals.CreateUser so the two can't diverge on
// how a password gets hashed. Runs the insert and every role assignment inside one
// Router.Begin(tenantID) transaction so a role assignment failing partway can't leave a user row
// committed with only some roles granted.
func createUserAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body createUserBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}

	user, err := peripherals.CreateUser(r.Context(), tx, tenantID, body.Email, body.Password)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeServiceError(w, http.StatusConflict, "email_taken", "A user with this email already exists.", nil)
			return
		}
		writeInternalError(w, err)
		return
	}

	assignedBy := parseOptionalUUID(rc.UserID)
	for _, role := range body.Roles {
		if err := peripherals.AssignRole(r.Context(), tx, tenantID, user.ID, role, assignedBy); err != nil {
			tx.Rollback(r.Context()) //nolint:errcheck
			writeInternalError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{"userId": user.ID, "email": user.Email, "roles": body.Roles},
	})
}

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

// listUsersAdmin is GET /admin/users — role assignments, a different shape from the plain
// {id, email} picker at GET /users (routes_users.go): operation name differs there too to avoid
// an OpenAPI/codegen collision, same reason this file's Rust original gives.
func listUsersAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	users, err := peripherals.ListUsers(r.Context(), tx, tenantID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck

	data := make([]map[string]any, len(users))
	for i, u := range users {
		data[i] = map[string]any{"userId": u.UserID, "roles": u.Roles}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type assignRoleBody struct {
	Role string `json:"role"`
}

func assignRoleAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "`userId` must be a UUID.", nil)
		return
	}
	var body assignRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	assignedBy := parseOptionalUUID(rc.UserID)
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	if err := peripherals.AssignRole(r.Context(), tx, tenantID, userID, body.Role, assignedBy); err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"userId": userID, "role": body.Role}})
}

func revokeRoleAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "`userId` must be a UUID.", nil)
		return
	}
	role := chi.URLParam(r, "role")
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	if err := peripherals.RevokeRole(r.Context(), tx, tenantID, userID, role); err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// invalidateContextAdmin is an operator's second option alongside just waiting out the TTL
// (control.ContextAttributesCache) after editing a user's membership record — a no-op (still
// 204) if the cache had nothing for this user.
func invalidateContextAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "`userId` must be a UUID.", nil)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if len(body.Roles) == 0 {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "`roles` must not be empty.", nil)
		return
	}
	actions := body.Actions
	if len(actions) == 0 {
		actions = knownActions()
	}
	for _, a := range actions {
		if !isKnownAction(a) {
			writeServiceError(w, http.StatusBadRequest, "validation_failed",
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
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "`id` must be a UUID.", nil)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	for _, g := range body.Grants {
		if !isKnownAction(g.Action) {
			writeServiceError(w, http.StatusBadRequest, "validation_failed",
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
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
	r.Get("/admin/users", wrap(listUsersAdmin))
	r.Post("/admin/users", wrap(createUserAdmin))
	r.Post("/admin/users/{userId}/roles", wrap(assignRoleAdmin))
	r.Delete("/admin/users/{userId}/roles/{role}", wrap(revokeRoleAdmin))
	r.Post("/admin/users/{userId}/context/invalidate", wrap(invalidateContextAdmin))
	r.Get("/admin/policies", wrap(listPoliciesAdmin))
	r.Post("/admin/policies", wrap(createPolicyAdmin))
	r.Post("/admin/policies/seed-defaults", wrap(seedDefaultPoliciesAdmin))
	r.Put("/admin/policies/matrix", wrap(syncMatrixPoliciesAdmin))
	r.Post("/admin/policies/explain", wrap(explainPolicyAdmin))
	r.Delete("/admin/policies/{id}", wrap(deletePolicyAdmin))
}
