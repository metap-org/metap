package roles

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// AuditEntity is the audit-trail entity name role administration records under.
const AuditEntity = kernel.AuditEntityRoles

// namePattern is what a new role may be called: it ends up in tokens' role lists, policy rows and
// URLs, so keep it to an identifier. Roles that predate the catalog keep whatever name they had.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// reservedNames can't be created: the permission matrix uses __everyone__ for "no role restriction".
var reservedNames = map[string]bool{"__everyone__": true}

// usageSampleSize caps how many holders/policies a role_in_use error lists by name.
const usageSampleSize = 5

// maxGrants bounds one SetPermissions call (entities × actions of a large app fit well inside).
const maxGrants = 5000

var workflowActionPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,100}$`)

func validationErr(msg string, fe map[string][]string) error {
	return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed, Message: msg, FieldErrors: fe}
}

// checkName is the 400 for a name a new role may not have, else nil. Pure.
func checkName(name string) error {
	if namePattern.MatchString(name) && !reservedNames[name] {
		return nil
	}
	return validationErr("A role name is lowercase letters, digits and underscores, starting with a letter (at most 80).",
		map[string][]string{"name": {"lowercase letters, digits and underscores, starting with a letter"}})
}

// storeErr maps the catalog's errors onto the API's; anything else is internal.
func storeErr(err error, name string) error {
	switch {
	case errors.Is(err, rolestore.ErrRoleNotFound):
		return kernel.Err(http.StatusNotFound, errcode.NotFound, "Role "+name+" not found.")
	case errors.Is(err, rolestore.ErrRoleExists):
		return &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RoleExists,
			Message: "A role named " + name + " already exists.", FieldErrors: map[string][]string{"name": {"already exists"}}}
	}
	return err
}

// view is a role as the API shows it. Pure.
func view(r rolestore.Role, users, policies int) *Role {
	return &Role{
		Id: r.ID.String(), Name: r.Name, Label: r.Label, Description: r.Description, Builtin: r.Builtin,
		UserCount: int32(users), PolicyCount: int32(policies),
		CreatedAt: kernel.FormatTime(&r.CreatedAt), UpdatedAt: kernel.FormatTime(&r.UpdatedAt),
	}
}

// validGrantAction is a base policy action, transition:<workflow action>, or action:<declared action>.
func validGrantAction(action string) bool {
	for _, a := range permission.AllEntityActions {
		if action == a.String() {
			return true
		}
	}
	for _, prefix := range []string{permission.TransitionActionPolicy(""), permission.ActionPolicy("")} {
		if strings.HasPrefix(action, prefix) && workflowActionPattern.MatchString(strings.TrimPrefix(action, prefix)) {
			return true
		}
	}
	return false
}

// checkGrants validates a replacement set and returns it trimmed, or the 400 naming the first bad
// permission. A virtual resource (roadmap 115 M6) takes only the actions it declares; anything else
// only the entity actions. Pure.
func checkGrants(role string, in []*RoleGrant) ([]rolestore.RoleGrant, error) {
	if role == permission.RoleAdmin {
		return nil, validationErr("The admin role bypasses every policy; it has no permissions to set.", nil)
	}
	if len(in) > maxGrants {
		return nil, validationErr("Too many permissions in one call.", nil)
	}
	out := make([]rolestore.RoleGrant, 0, len(in))
	for _, g := range in {
		entity, action := strings.TrimSpace(g.GetEntity()), strings.TrimSpace(g.GetAction())
		valid := validGrantAction(action)
		if strings.HasPrefix(entity, permission.ResourcePrefix) {
			valid = permission.ValidResourceGrant(entity, action)
		}
		if entity == "" || len(entity) > 120 || !valid {
			return nil, validationErr("Invalid permission "+entity+" "+action+".", map[string][]string{"grants": {entity + " " + action}})
		}
		out = append(out, rolestore.RoleGrant{Entity: entity, Action: action})
	}
	return out, nil
}

func grantKey(g rolestore.RoleGrant) string { return g.Entity + " " + g.Action }

// grantDiff lists, as "entity action", what after adds to and drops from before. Pure.
func grantDiff(before, after []rolestore.RoleGrant) (added, removed []string) {
	was, now := map[string]bool{}, map[string]bool{}
	for _, g := range before {
		was[grantKey(g)] = true
	}
	for _, g := range after {
		k := grantKey(g)
		now[k] = true
		if !was[k] {
			added = append(added, k)
		}
	}
	for k := range was {
		if !now[k] {
			removed = append(removed, k)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}

// permissionsView is a role's permissions as the API shows them. Pure.
func permissionsView(role string, grants []rolestore.RoleGrant, advanced []rolestore.AdvancedPolicy) *RolePermissions {
	out := &RolePermissions{Role: role}
	for _, g := range grants {
		out.Grants = append(out.Grants, &RoleGrant{Entity: g.Entity, Action: g.Action})
	}
	for _, p := range advanced {
		out.Advanced = append(out.Advanced, &RolePolicy{Id: p.ID.String(), Entity: p.Entity, Action: p.Action,
			Roles: p.Roles, Effect: p.Effect, Field: p.Field, Subject: p.Subject, Condition: kernel.JSONOr(p.Condition, "null")})
	}
	return out
}

// inUseError is role_in_use with what blocks the delete: fieldErrors.members (emails, already
// sampled) and fieldErrors.policies ("entity action"), each capped at usageSampleSize plus a "+N
// more" entry. Pure.
func inUseError(name string, holders int, emails []string, policies []rolestore.PolicyRef) error {
	fields := map[string][]string{}
	if holders > 0 {
		fields["members"] = append(fields["members"], emails...)
		if holders > len(emails) {
			fields["members"] = append(fields["members"], fmt.Sprintf("+%d", holders-len(emails)))
		}
	}
	for i, p := range policies {
		if i == usageSampleSize {
			fields["policies"] = append(fields["policies"], fmt.Sprintf("+%d", len(policies)-usageSampleSize))
			break
		}
		fields["policies"] = append(fields["policies"], p.Entity+" "+p.Action)
	}
	return &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RoleInUse,
		Message: fmt.Sprintf("Role %s is still in use: %d member(s) hold it and %d polic(ies) name it.",
			name, holders, len(policies)),
		FieldErrors: fields}
}
