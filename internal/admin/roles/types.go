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

func (x *PermissionResourceAction) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *PermissionResourceAction) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *PermissionResource) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *PermissionResource) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *PermissionResource) GetKey() string {
	if x != nil {
		return x.Key
	}
	return ""
}

func (x *PermissionResource) GetActions() []*PermissionResourceAction {
	if x != nil {
		return x.Actions
	}
	return nil
}

func (x *ListPermissionResourcesResponse) GetResources() []*PermissionResource {
	if x != nil {
		return x.Resources
	}
	return nil
}

func (x *SetRolePermissionsRequest) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *SetRolePermissionsRequest) GetGrants() []*RoleGrant {
	if x != nil {
		return x.Grants
	}
	return nil
}

func (x *RolePolicy) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *RolePolicy) GetEntity() string {
	if x != nil {
		return x.Entity
	}
	return ""
}

func (x *RolePolicy) GetAction() string {
	if x != nil {
		return x.Action
	}
	return ""
}

func (x *RolePolicy) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *RolePolicy) GetEffect() string {
	if x != nil {
		return x.Effect
	}
	return ""
}

func (x *RolePolicy) GetField() string {
	if x != nil {
		return x.Field
	}
	return ""
}

func (x *RolePolicy) GetSubject() string {
	if x != nil {
		return x.Subject
	}
	return ""
}

func (x *RolePolicy) GetCondition() json.RawMessage {
	if x != nil {
		return x.Condition
	}
	return nil
}

func (x *RoleGrant) GetEntity() string {
	if x != nil {
		return x.Entity
	}
	return ""
}

func (x *RoleGrant) GetAction() string {
	if x != nil {
		return x.Action
	}
	return ""
}

func (x *RolePermissions) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *RolePermissions) GetGrants() []*RoleGrant {
	if x != nil {
		return x.Grants
	}
	return nil
}

func (x *RolePermissions) GetAdvanced() []*RolePolicy {
	if x != nil {
		return x.Advanced
	}
	return nil
}

func (x *ListRoleAuditRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *ListRoleAuditRequest) GetCursor() string {
	if x != nil {
		return x.Cursor
	}
	return ""
}

func (x *ListRoleAuditRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

func (x *UpdateRoleRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *UpdateRoleRequest) GetLabel() string {
	if x != nil {
		return x.Label
	}
	return ""
}

func (x *UpdateRoleRequest) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *CreateRoleRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *CreateRoleRequest) GetLabel() string {
	if x != nil {
		return x.Label
	}
	return ""
}

func (x *CreateRoleRequest) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *CreateRoleRequest) GetCopyFrom() string {
	if x != nil {
		return x.CopyFrom
	}
	return ""
}

func (x *RoleNameRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *ListRolesResponse) GetRoles() []*Role {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *ListRolesRequest) GetSearch() string {
	if x != nil {
		return x.Search
	}
	return ""
}

func (x *Role) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Role) GetUserCount() int32 {
	if x != nil {
		return x.UserCount
	}
	return 0
}

func (x *Role) GetBuiltin() bool {
	if x != nil {
		return x.Builtin
	}
	return false
}

func (x *Role) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Role) GetLabel() string {
	if x != nil {
		return x.Label
	}
	return ""
}

func (x *Role) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *Role) GetPolicyCount() int32 {
	if x != nil {
		return x.PolicyCount
	}
	return 0
}

func (x *Role) GetCreatedAt() string {
	if x != nil {
		return x.CreatedAt
	}
	return ""
}

func (x *Role) GetUpdatedAt() string {
	if x != nil {
		return x.UpdatedAt
	}
	return ""
}
