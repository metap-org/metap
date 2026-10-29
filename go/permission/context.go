package permission

import (
	"encoding/json"
	"strings"
)

const (
	OAuthScopeAttribute    = "oauthScope"
	OAuthClientIDAttribute = "oauthClientId"
	AdminScope             = "admin"
)

// RequestContext mirrors the Rust RequestContext.
type RequestContext struct {
	TenantID          string
	UserID            string // "" means absent
	Roles             []string
	HasRoles          bool
	FunctionID        string
	ContextAttributes map[string]json.RawMessage // nil means absent
	// ForwardedBearerToken is a transport concern for a gRPC client, never ABAC-visible —
	// deliberately excluded from ToValue(), matching the Rust #[serde(skip)].
	ForwardedBearerToken string
}

func (c *RequestContext) IsAdmin() bool {
	for _, r := range c.Roles {
		if r == "admin" {
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

// ScopePermits: see the Rust RequestContext::scope_permits doc comment for the grammar.
func (c *RequestContext) ScopePermits(entity string, action EntityAction) bool {
	tokens, ok := c.OAuthScope()
	if !ok {
		return true
	}
	var verb string
	switch action {
	case ActionRead:
		verb = "read"
	case ActionCreate, ActionUpdate:
		verb = "write"
	case ActionDelete:
		verb = "delete"
	case ActionTransition:
		verb = "transition"
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

// ToValue serializes the context to a JSON-object-shaped value, with ContextAttributes
// flattened to the top level — matches the Rust #[serde(flatten)] behavior.
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
		out[k] = v
	}
	b, _ := json.Marshal(out)
	return b
}

// PermissionDecision mirrors the Rust PermissionDecision.
type PermissionDecision struct {
	Allowed bool
	Reason  string
	Field   string
}

func Allowed() PermissionDecision { return PermissionDecision{Allowed: true} }
func Forbidden() PermissionDecision {
	return PermissionDecision{Allowed: false, Reason: "forbidden"}
}
func ForbiddenField(field string) PermissionDecision {
	return PermissionDecision{Allowed: false, Reason: "forbidden", Field: field}
}

// EntityAction is one of the 5 actions a policy can grant.
type EntityAction int

const (
	ActionRead EntityAction = iota
	ActionCreate
	ActionUpdate
	ActionDelete
	ActionTransition
)

var AllEntityActions = [5]EntityAction{ActionRead, ActionCreate, ActionUpdate, ActionDelete, ActionTransition}

func (a EntityAction) String() string {
	switch a {
	case ActionRead:
		return "read"
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	case ActionTransition:
		return "transition"
	default:
		return "unknown"
	}
}
