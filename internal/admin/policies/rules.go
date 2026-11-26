package policies

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntity is the audit-trail entity policy administration records under.
const AuditEntity = kernel.AuditEntityPolicies

// fieldsOf is the before/after view of a policy for an audit entry; gone is true for a policy that
// is going away. Pure.
func fieldsOf(row permission.PolicyRow, gone bool) map[string][2]any {
	pair := func(v any) [2]any {
		if gone {
			return [2]any{v, nil}
		}
		return [2]any{nil, v}
	}
	diff := map[string][2]any{
		"entity": pair(row.Entity), "action": pair(row.Action), "effect": pair(row.Effect.String()), "subject": pair(row.Subject),
	}
	if row.HasRoles {
		diff["roles"] = pair(row.Roles)
	}
	if row.Field != "" {
		diff["field"] = pair(row.Field)
	}
	if row.Condition != nil {
		diff["condition"] = pair(row.Condition)
	}
	return diff
}

// basicGrants lists a set of policy rows as sorted "role:action" strings, the part of an entity's
// policies the permission matrix owns (no condition, field, record subject or deny): what a sync
// replaces, and so what an audit entry for it shows. Pure.
func basicGrants(rows []permission.PolicyRow) []string {
	out := []string{}
	for _, r := range rows {
		if r.Condition != nil || r.Field != "" || r.Subject != "context" || r.Effect != permission.EffectAllow {
			continue
		}
		role := "*"
		if r.HasRoles {
			role = strings.Join(r.Roles, ",")
		}
		out = append(out, role+":"+r.Action)
	}
	sort.Strings(out)
	return out
}

// view is a policy as the API shows it. Pure.
func view(row permission.PolicyRow) *Policy {
	p := &Policy{ID: row.ID.String(), TenantID: row.TenantID.String(), Entity: row.Entity, Action: row.Action,
		Subject: row.Subject, Effect: row.Effect.String(), Condition: kernel.JSONOr(nil, "null")}
	if row.Field != "" {
		p.Field = &row.Field
	}
	if row.HasRoles {
		p.Roles = row.Roles
		if p.Roles == nil {
			p.Roles = []string{}
		}
	}
	if row.Condition != nil {
		if raw, err := json.Marshal(row.Condition); err == nil {
			p.Condition = raw
		}
	}
	if row.CreatedBy != nil {
		s := row.CreatedBy.String()
		p.CreatedBy = &s
	}
	return p
}

func views(rows []permission.PolicyRow) []*Policy {
	out := make([]*Policy, len(rows))
	for i, row := range rows {
		out[i] = view(row)
	}
	return out
}

func validationErr(msg string) error {
	return kernel.Err(kernel.StatusBadRequest, errcode.ValidationFailed, msg)
}

func unknownAction(action string) error {
	return validationErr("Unknown action \"" + action + "\" — must be one of [\"" + strings.Join(permission.EntityActionNames(), "\", \"") + "\"].")
}

func optionalUUID(s string) *uuid.UUID {
	id, err := uuid.Parse(s)
	if s == "" || err != nil {
		return nil
	}
	return &id
}

// newPolicy checks a creation request and returns what to create, or the 400. Pure.
func newPolicy(req *CreatePolicyRequest, by *uuid.UUID) (NewPolicy, error) {
	var cond *permission.PolicyCondition
	if len(req.Condition) > 0 && string(req.Condition) != "null" {
		cond = &permission.PolicyCondition{}
		if err := json.Unmarshal(req.Condition, cond); err != nil {
			return NewPolicy{}, validationErr("`condition` is not a valid condition: " + err.Error())
		}
	}
	p := NewPolicy{Entity: req.Entity, Action: req.Action, Roles: req.Roles, HasRoles: req.Roles != nil, Condition: cond, By: by,
		Subject: permission.SubjectContext, Effect: permission.EffectAllow}
	if req.Subject != nil && *req.Subject == "record" {
		p.Subject = permission.SubjectRecord
	}
	if req.Effect != nil {
		p.Effect = permission.ParsePolicyEffect(*req.Effect)
	}
	if req.Field != nil {
		p.Field = *req.Field
	}
	return p, nil
}

// seedActions checks a seed request and returns the actions to create a policy for (every entity
// action when none are named), or the 400. Pure.
func seedActions(req *SeedDefaultPoliciesRequest) ([]string, error) {
	if len(req.Roles) == 0 {
		return nil, validationErr("`roles` must not be empty.")
	}
	actions := req.Actions
	if len(actions) == 0 {
		actions = permission.EntityActionNames()
	}
	for _, a := range actions {
		if !permission.IsEntityAction(a) {
			return nil, unknownAction(a)
		}
	}
	return actions, nil
}

// matrixGrants checks a matrix save and returns the grants, or the 400. Pure.
func matrixGrants(in []MatrixGrant) ([]permission.RoleActionGrant, error) {
	out := make([]permission.RoleActionGrant, len(in))
	for i, g := range in {
		if !permission.IsEntityAction(g.Action) {
			return nil, unknownAction(g.Action)
		}
		role := ""
		if g.Role != nil {
			role = *g.Role
		}
		out[i] = permission.RoleActionGrant{Role: role, HasRole: g.Role != nil, Action: g.Action}
	}
	return out, nil
}
