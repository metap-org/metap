// Scoped role assignments (roadmap 115, M1). A role can be held for the whole tenant (user_roles,
// RequestContext.Roles) or only inside a scope — "analyst on zone Z", "viewer on account A". An
// entity declares which scopes its records belong to (ScopeBinding); a scoped role then grants
// its permissions only on records inside one of its scopes.
//
// The rule that keeps this safe: access is the OR of "role groups". The tenant-wide group (every
// record) holds the tenant-wide roles. Each scoped group holds those roles plus whatever it was
// given at specific scopes, and applies only to records inside those scopes. A record is
// reachable iff some group both allows the action on the entity and covers the record, and the
// record-level policies allow it under THAT group's roles — never the union of every role the
// user holds anywhere, which would let a role granted on scope A leak onto scope B.
package permission

import (
	"encoding/json"
	"sort"
	"strings"
)

// ScopeBinding says how a record of an entity belongs to a scope: the record is in scope
// (Type, id) when its Field equals id — or, with Array, when the field is a list holding id.
// Field "id" is the record's own id (the scope entity itself, e.g. a Zone is in scope "zone").
type ScopeBinding struct {
	Type  string `json:"type"`
	Field string `json:"field"`
	Array bool   `json:"array,omitempty"`
}

// ScopedRole is a role held only inside one scope.
type ScopedRole struct {
	Role      string
	ScopeType string
	ScopeID   string
}

// ScopeRef is one scope a group covers.
type ScopeRef struct {
	Type string
	ID   string
}

// RoleGroup is a role set and the scopes it applies to. Scopes empty means the whole tenant.
type RoleGroup struct {
	Roles  []string
	Scopes []ScopeRef
}

// Wide reports whether the group applies to every record.
func (g RoleGroup) Wide() bool { return len(g.Scopes) == 0 }

// WithRoles returns a copy of the context holding exactly roles (and no scoped roles), so the
// ordinary policy evaluation can run unchanged for one group.
func (c *RequestContext) WithRoles(roles []string) *RequestContext {
	cp := *c
	cp.Roles = roles
	cp.HasRoles = true
	cp.ScopedRoles = nil
	return &cp
}

// RoleGroups returns the tenant-wide group first, then one group per distinct role set that
// scoped assignments produce; scopes that give the same role set share a group, so a user with
// one role on a thousand zones is a single group, not a thousand.
func (c *RequestContext) RoleGroups() []RoleGroup {
	groups := []RoleGroup{{Roles: append([]string(nil), c.Roles...)}}
	if len(c.ScopedRoles) == 0 {
		return groups
	}
	byScope := map[ScopeRef][]string{}
	for _, sr := range c.ScopedRoles {
		ref := ScopeRef{Type: sr.ScopeType, ID: sr.ScopeID}
		byScope[ref] = append(byScope[ref], sr.Role)
	}
	byRoleSet := map[string]*RoleGroup{}
	var order []string
	refs := make([]ScopeRef, 0, len(byScope))
	for ref := range byScope {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].ID < refs[j].ID
	})
	for _, ref := range refs {
		set := dedupSorted(append(append([]string(nil), c.Roles...), byScope[ref]...))
		key := strings.Join(set, "\x00")
		g, ok := byRoleSet[key]
		if !ok {
			g = &RoleGroup{Roles: set}
			byRoleSet[key] = g
			order = append(order, key)
		}
		g.Scopes = append(g.Scopes, ref)
	}
	for _, key := range order {
		groups = append(groups, *byRoleSet[key])
	}
	return groups
}

func dedupSorted(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// Covers reports whether a record is inside one of the group's scopes, per the entity's
// bindings. recordID is the record's own id (what a binding on field "id" compares).
func (g RoleGroup) Covers(bindings []ScopeBinding, recordID string, record JsonObject) bool {
	if g.Wide() {
		return true
	}
	for _, b := range bindings {
		for _, ref := range g.Scopes {
			if ref.Type != b.Type {
				continue
			}
			if recordInScope(b, ref.ID, recordID, record) {
				return true
			}
		}
	}
	return false
}

func recordInScope(b ScopeBinding, scopeID, recordID string, record JsonObject) bool {
	if b.Field == "id" {
		return recordID == scopeID
	}
	raw, ok := record[b.Field]
	if !ok {
		return false
	}
	if b.Array {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return false
		}
		for _, it := range items {
			var s string
			if json.Unmarshal(it, &s) == nil && s == scopeID {
				return true
			}
		}
		return false
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == scopeID
}

// ScopeIDsFor returns the scope ids of the group that apply to an entity binding of type typ.
func (g RoleGroup) ScopeIDsFor(typ string) []string {
	var ids []string
	for _, ref := range g.Scopes {
		if ref.Type == typ {
			ids = append(ids, ref.ID)
		}
	}
	return ids
}
