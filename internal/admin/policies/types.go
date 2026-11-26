package policies

import "encoding/json"

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

// A policy, as the permission matrix and the advanced policy editor see it.
type Policy struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Entity   string `json:"entity" required:"false"`
	Action   string `json:"action" required:"false"`
	// Nil when the policy covers the whole record, not one field.
	Field *string `json:"field"`
	// "context" (about the caller) or "record" (about the record).
	Subject string `json:"subject"`
	// Nil when the policy applies to every role.
	Roles     []string        `json:"roles" required:"false"`
	Condition json.RawMessage `json:"condition" required:"false"`
	CreatedBy *string         `json:"createdBy"`
	// "allow" or "deny".
	Effect string `json:"effect"`
}

type PolicyList struct {
	Data []*Policy `json:"data"`
}

type PolicyOne struct {
	Data *Policy `json:"data"`
}

type ListPoliciesRequest struct {
	Entity string
}

type CreatePolicyRequest struct {
	Entity    string          `json:"entity" required:"false"`
	Action    string          `json:"action" required:"false"`
	Roles     []string        `json:"roles,omitempty"`
	Condition json.RawMessage `json:"condition,omitempty"`
	Field     *string         `json:"field,omitempty"`
	Subject   *string         `json:"subject,omitempty"`
	Effect    *string         `json:"effect,omitempty"`
}

type SeedDefaultPoliciesRequest struct {
	Entity  string   `json:"entity" required:"false"`
	Roles   []string `json:"roles" required:"false"`
	Actions []string `json:"actions,omitempty"`
}

type MatrixGrant struct {
	// Nil grants the action to everyone.
	Role   *string `json:"role"`
	Action string  `json:"action" required:"false"`
}

type SyncMatrixRequest struct {
	Entity string        `json:"entity" required:"false"`
	Grants []MatrixGrant `json:"grants" required:"false"`
}

type PolicyIDRequest struct {
	ID string
}

type UserIDRequest struct {
	UserID string
}

type ExplainPolicyRequest struct {
	Entity string                     `json:"entity" required:"false"`
	Action string                     `json:"action" required:"false"`
	Field  *string                    `json:"field,omitempty"`
	Record map[string]json.RawMessage `json:"record,omitempty"`
}

type ExplainResponse struct {
	Data json.RawMessage `json:"data"`
}
