package roles

import "encoding/json"

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

// One role of the tenant's catalog (roadmap 114).
type Role struct {
	// The key members and policies refer to; never changes.
	Name string `json:"name" required:"false"`
	// Active members holding the role.
	UserCount int32 `json:"userCount" required:"false"`
	// Defined by the platform (admin, platform_admin): can't be deleted.
	Builtin bool   `json:"builtin" required:"false"`
	Id      string `json:"id" required:"false"`
	// "" = no label set; clients then show the name (translated if they know it).
	Label       string `json:"label" required:"false"`
	Description string `json:"description" required:"false"`
	// Policies naming the role, basic and advanced.
	PolicyCount int32  `json:"policyCount" required:"false"`
	CreatedAt   string `json:"createdAt" required:"false"`
	UpdatedAt   string `json:"updatedAt" required:"false"`
}

type ListRolesRequest struct {
	// Case-insensitive substring of name, label or description.
	Search string `json:"search" required:"false"`
}

type ListRolesResponse struct {
	Roles []*Role `json:"roles" required:"false"`
}

type RoleNameRequest struct {
	Name string `json:"name" required:"false"`
}

type CreateRoleRequest struct {
	// Lowercase letters, digits and underscores, starting with a letter; at most 80.
	Name        string `json:"name" required:"false"`
	Label       string `json:"label" required:"false"`
	Description string `json:"description" required:"false"`
	// Starts the new role with this existing role's basic permissions (not its members).
	CopyFrom string `json:"copyFrom" required:"false"`
}

type UpdateRoleRequest struct {
	Name        string `json:"name" required:"false"`
	Label       string `json:"label" required:"false"`
	Description string `json:"description" required:"false"`
}

type ListRoleAuditRequest struct {
	Name   string `json:"name" required:"false"`
	Cursor string `json:"cursor" required:"false"`
	Limit  int32  `json:"limit" required:"false"`
}

type RolePermissions struct {
	Role     string        `json:"role" required:"false"`
	Grants   []*RoleGrant  `json:"grants" required:"false"`
	Advanced []*RolePolicy `json:"advanced" required:"false"`
}

// One basic permission: the role may do action on entity. action is read, create, update, delete,
// transition (every workflow action) or transition:<workflow action> (that one only).
type RoleGrant struct {
	Entity string `json:"entity" required:"false"`
	Action string `json:"action" required:"false"`
}

// A policy naming the role that isn't a plain grant (a condition, a field, the record subject, or a
// deny). Shown beside the grants; edited in the policy editor.
type RolePolicy struct {
	Id     string   `json:"id" required:"false"`
	Entity string   `json:"entity" required:"false"`
	Action string   `json:"action" required:"false"`
	Roles  []string `json:"roles" required:"false"`
	// "allow" or "deny".
	Effect string `json:"effect" required:"false"`
	Field  string `json:"field" required:"false"`
	// "context" or "record".
	Subject string `json:"subject" required:"false"`
	// JSON, "" when the policy has no condition.
	Condition json.RawMessage `json:"condition" required:"false"`
}

type SetRolePermissionsRequest struct {
	Role string `json:"role" required:"false"`
	// The role's complete set of basic permissions after the call.
	Grants []*RoleGrant `json:"grants" required:"false"`
}

type ListPermissionResourcesResponse struct {
	Resources []*PermissionResource `json:"resources" required:"false"`
}

type PermissionResource struct {
	Name        string `json:"name" required:"false"`
	Description string `json:"description" required:"false"`
	// The "entity" value to use in a RoleGrant for this resource.
	Key     string                      `json:"key" required:"false"`
	Actions []*PermissionResourceAction `json:"actions" required:"false"`
}

type PermissionResourceAction struct {
	Name        string `json:"name" required:"false"`
	Description string `json:"description" required:"false"`
}
