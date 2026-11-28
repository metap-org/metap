package permission

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/metap-org/metap/pkg/kit/errcode"
)

const (
	OAuthScopeAttribute    = "oauthScope"
	OAuthClientIDAttribute = "oauthClientId"
	AdminScope             = "admin"
)

type RequestContext struct {
	TenantID string
	UserID   string // "" means absent
	Roles    []string
	HasRoles bool
	// ScopedRoles are roles held only inside a scope (roadmap 115 M1). Roles above stay the
	// tenant-wide ones: IsAdmin and every existing check look at them alone.
	ScopedRoles       []ScopedRole
	FunctionID        string
	ContextAttributes map[string]json.RawMessage // nil means absent
	// ForwardedBearerToken is for calling another service as this caller (pkg/recordclient), never ABAC-visible — deliberately
	// excluded from ToValue().
	ForwardedBearerToken string
	// SessionID is the login session behind the request (roadmap 115 M4); "" for a token that is not
	// a login session (API key, OAuth2, service token). Not ABAC-visible.
	SessionID string
}

func (c *RequestContext) IsAdmin() bool {
	for _, r := range c.Roles {
		if r == RoleAdmin {
			return true
		}
	}
	return false
}

// ApplyOAuthGrant folds an OAuth2 grant into ContextAttributes.
func (c *RequestContext) ApplyOAuthGrant(scope string, clientID string, hasClientID bool) {
	if c.ContextAttributes == nil {
		c.ContextAttributes = map[string]json.RawMessage{}
	}
	tokens := strings.Fields(scope)
	b, _ := json.Marshal(tokens)
	c.ContextAttributes[OAuthScopeAttribute] = b
	if hasClientID {
		cb, _ := json.Marshal(clientID)
		c.ContextAttributes[OAuthClientIDAttribute] = cb
	}
}

// OAuthScope returns the granted scope tokens, or (nil, false) for an ordinary session.
func (c *RequestContext) OAuthScope() ([]string, bool) {
	if c.ContextAttributes == nil {
		return nil, false
	}
	raw, ok := c.ContextAttributes[OAuthScopeAttribute]
	if !ok {
		return nil, false
	}
	var tokens []string
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return nil, false
	}
	return tokens, true
}

func (c *RequestContext) ScopePermits(entity string, action EntityAction) bool {
	tokens, ok := c.OAuthScope()
	if !ok {
		return true
	}
	var verb string
	switch action {
	case ActionRead:
		verb = PolicyActionRead
	case ActionCreate, ActionUpdate:
		verb = "write"
	case ActionDelete:
		verb = PolicyActionDelete
	case ActionTransition:
		verb = PolicyActionTransition
	}
	for _, t := range tokens {
		v, e, found := strings.Cut(t, ":")
		if found && v == verb && (e == "*" || e == entity) {
			return true
		}
	}
	return false
}

func (c *RequestContext) ScopePermitsAdminSurface() bool {
	tokens, ok := c.OAuthScope()
	if !ok {
		return true
	}
	for _, t := range tokens {
		if t == AdminScope {
			return true
		}
	}
	return false
}

// ToValue serializes the context to a JSON-object-shaped value, with ContextAttributes flattened to
// the top level.
func (c *RequestContext) ToValue() json.RawMessage {
	out := map[string]json.RawMessage{}
	tenantID, _ := json.Marshal(c.TenantID)
	out["tenantId"] = tenantID
	if c.UserID != "" {
		b, _ := json.Marshal(c.UserID)
		out["userId"] = b
	}
	if c.HasRoles {
		b, _ := json.Marshal(c.Roles)
		out["roles"] = b
	}
	if c.FunctionID != "" {
		b, _ := json.Marshal(c.FunctionID)
		out["functionId"] = b
	}
	for k, v := range c.ContextAttributes {
		switch k {
		case "tenantId", "userId", "roles", "functionId":
			// What the request says about the caller is not an attribute's to replace.
		default:
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}

type PermissionDecision struct {
	Allowed bool
	Reason  string
	Field   string
}

func Allowed() PermissionDecision { return PermissionDecision{Allowed: true} }
func Forbidden() PermissionDecision {
	return PermissionDecision{Allowed: false, Reason: errcode.Forbidden}
}
func ForbiddenField(field string) PermissionDecision {
	return PermissionDecision{Allowed: false, Reason: errcode.Forbidden, Field: field}
}

// EntityAction is one of the 5 actions a policy can grant.
// RoleAdmin is the built-in role that bypasses every policy within its tenant.
const RoleAdmin = "admin"

// The policy action names a policy row's Action column holds (EntityAction.String()).
const (
	PolicyActionRead       = "read"
	PolicyActionCreate     = "create"
	PolicyActionUpdate     = "update"
	PolicyActionDelete     = "delete"
	PolicyActionTransition = "transition"
)

type EntityAction int

const (
	ActionRead EntityAction = iota
	ActionCreate
	ActionUpdate
	ActionDelete
	ActionTransition
)

var AllEntityActions = [5]EntityAction{ActionRead, ActionCreate, ActionUpdate, ActionDelete, ActionTransition}

// TransitionActionPolicy is the policy action name that grants or denies a single workflow
// action, e.g. `transition:approve`. A policy on the plain `transition` action still covers
// every action. (Unrelated to the OAuth2 scope grammar `transition:<entity>`, which narrows a
// token by entity, not by workflow action.)
func TransitionActionPolicy(action string) string { return ActionTransition.String() + ":" + action }

// ActionPolicy is the policy action that grants or denies running one declared entity action (a
// custom action such as "verifyDns"): policies on it decide who may run it, in place of the roles
// the action declares. Unrelated to the five entity actions and to `transition:<workflow action>`.
func ActionPolicy(name string) string { return "action:" + name }

func (a EntityAction) String() string {
	switch a {
	case ActionRead:
		return PolicyActionRead
	case ActionCreate:
		return PolicyActionCreate
	case ActionUpdate:
		return PolicyActionUpdate
	case ActionDelete:
		return PolicyActionDelete
	case ActionTransition:
		return PolicyActionTransition
	default:
		return "unknown"
	}
}

// EntityActionNames are the names of AllEntityActions, in order: what a policy's action may be.
func EntityActionNames() []string {
	out := make([]string, len(AllEntityActions))
	for i, a := range AllEntityActions {
		out[i] = a.String()
	}
	return out
}

// IsEntityAction reports whether name is one of AllEntityActions.
func IsEntityAction(name string) bool {
	return slices.Contains(EntityActionNames(), name)
}
