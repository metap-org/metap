// Record visibility for a caller holding roles inside scopes (roadmap 115 M1), as SQL.
package query

import (
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// PlanOption adjusts how a list or aggregate query restricts the records a caller may see.
type PlanOption func(*planOptions)

type planOptions struct {
	access *permission.AccessPlan
}

// WithAccessPlan restricts the query by the caller's role groups instead of by their tenant-wide
// roles alone. Callers pass it only when permission.PermissionSnapshot.PlanAccess says the caller
// holds scoped roles; without it the query is built exactly as before.
func WithAccessPlan(plan permission.AccessPlan) PlanOption {
	return func(o *planOptions) { o.access = &plan }
}

// recordVisibility is the WHERE fragment limiting a query to the records the caller may read:
// the ordinary record-policy clause, or, with an access plan, the OR over the caller's role
// groups.
func recordVisibility(entity metadata.EntityDefinition, policies []permission.PolicyRow, rc *permission.RequestContext,
	params *ParamBuilder, opts []PlanOption) (string, bool, error) {
	var o planOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.access == nil {
		return RecordPolicyWhereClause(policies, rc, params)
	}
	return scopedRecordWhere(entity, *o.access, policies, rc, params)
}

// scopedRecordWhere ORs one clause per role group that may read: the tenant-wide group (its
// record policies, or nothing at all if it has none, which makes the whole query unrestricted)
// and each scoped group (its scope predicate AND its own record policies). No clause at all
// means nothing is readable.
func scopedRecordWhere(entity metadata.EntityDefinition, plan permission.AccessPlan, policies []permission.PolicyRow,
	rc *permission.RequestContext, params *ParamBuilder) (string, bool, error) {
	var parts []string
	if plan.Wide {
		wide, restricted, err := RecordPolicyWhereClause(policies, rc.WithRoles(rc.Roles), params)
		if err != nil {
			return "", false, err
		}
		if !restricted {
			return "", false, nil // the tenant-wide group already reaches every record
		}
		parts = append(parts, "("+wide+")")
	}
	for _, g := range plan.Groups {
		pred := scopePredicate(entity, g.Group, params)
		if pred == "" {
			continue // none of the group's scope types applies to this entity
		}
		part := pred
		rec, restricted, err := RecordPolicyWhereClause(policies, g.Ctx, params)
		if err != nil {
			return "", false, err
		}
		if restricted {
			part = pred + " AND (" + rec + ")"
		}
		parts = append(parts, "("+part+")")
	}
	if len(parts) == 0 {
		return "false", true, nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", true, nil
}

// scopePredicate is "the record is inside one of the group's scopes" for the entity's bindings,
// "" when no binding matches a scope type the group holds.
func scopePredicate(entity metadata.EntityDefinition, g permission.RoleGroup, params *ParamBuilder) string {
	var any []string
	for _, b := range entity.Scopes {
		ids := g.ScopeIDsFor(b.Type)
		if len(ids) == 0 {
			continue
		}
		switch {
		case b.Field == "id" || scopeFieldIsUUID(entity, b.Field):
			// A scope id that is not a UUID can match no uuid column; dropping it is exact.
			var us []uuid.UUID
			for _, id := range ids {
				if u, err := uuid.Parse(id); err == nil {
					us = append(us, u)
				}
			}
			if len(us) == 0 {
				continue
			}
			col := "id"
			if b.Field != "id" {
				col = `"` + b.Field + `"`
			}
			any = append(any, col+" = ANY("+params.Push(UUIDArrayBind(us))+")")
		case b.Array:
			any = append(any, `"`+b.Field+`" ?| `+params.Push(TextArrayBind(ids)))
		default:
			any = append(any, `"`+b.Field+`"::text = ANY(`+params.Push(TextArrayBind(ids))+")")
		}
	}
	if len(any) == 0 {
		return ""
	}
	return "(" + strings.Join(any, " OR ") + ")"
}

func scopeFieldIsUUID(entity metadata.EntityDefinition, field string) bool {
	for _, f := range entity.Fields {
		if f.Name == field {
			return f.Kind == metadata.KindReference || f.Kind == metadata.KindID
		}
	}
	return false
}
