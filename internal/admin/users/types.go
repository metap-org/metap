package users

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

// A member of the caller's tenant.
type User struct {
	Id          string `json:"id" required:"false"`
	Email       string `json:"email" required:"false"`
	DisplayName string `json:"displayName" required:"false"`
	// "active", "disabled" or "invited".
	Status string   `json:"status" required:"false"`
	Roles  []string `json:"roles" required:"false"`
	// "local", or the external IdP that provisioned the member ("oidc", "oauth2").
	AuthProvider string `json:"authProvider" required:"false"`
	HasPassword  bool   `json:"hasPassword" required:"false"`
	LastLoginAt  string `json:"lastLoginAt" required:"false"`
	CreatedAt    string `json:"createdAt" required:"false"`
	InvitedBy    string `json:"invitedBy" required:"false"`
	// True when the person is also a member of another tenant: their password is not this
	// tenant's to reset.
	SharedIdentity bool `json:"sharedIdentity" required:"false"`
	// For an invited member: when the open invitation expires ("" when none is open).
	InvitationExpiresAt string `json:"invitationExpiresAt" required:"false"`
	// Roles held only inside a scope (roles above are tenant-wide).
	ScopedRoles []*ScopedRole `json:"scopedRoles" required:"false"`
	// The tenant's owner (roadmap 115 M8): protected from other admins.
	IsOwner bool `json:"isOwner" required:"false"`
}

// A role held only on the records of one scope (roadmap 115 M1): "analyst on zone Z".
type ScopedRole struct {
	Role      string `json:"role" required:"false"`
	ScopeType string `json:"scopeType" required:"false"`
	ScopeId   string `json:"scopeId" required:"false"`
}

type ListUsersRequest struct {
	// Case-insensitive substring of email or display name.
	Search string `json:"search" required:"false"`
	Status string `json:"status" required:"false"`
	Role   string `json:"role" required:"false"`
	Offset int32  `json:"offset" required:"false"`
	// 1..200, default 50.
	Limit int32 `json:"limit" required:"false"`
}

type ListUsersResponse struct {
	Users []*User `json:"users" required:"false"`
	Total int32   `json:"total" required:"false"`
}

type UserIdRequest struct {
	Id string `json:"id" required:"false"`
}

type CreateUserRequest struct {
	Email string `json:"email" required:"false"`
	// Used only when the person has no account yet; ignored (not an error) otherwise.
	Password    string   `json:"password" required:"false"`
	DisplayName string   `json:"displayName" required:"false"`
	Roles       []string `json:"roles" required:"false"`
}

type CreateUserResponse struct {
	User *User `json:"user" required:"false"`
	// False when the email already had an account (added to this tenant, password unchanged).
	CreatedIdentity bool `json:"createdIdentity" required:"false"`
}

type InviteUserRequest struct {
	Email       string   `json:"email" required:"false"`
	DisplayName string   `json:"displayName" required:"false"`
	Roles       []string `json:"roles" required:"false"`
}

type InviteUserResponse struct {
	// The invited member (status "invited" until they accept).
	User      *User  `json:"user" required:"false"`
	ExpiresAt string `json:"expiresAt" required:"false"`
	// False when no email went out (SMTP not configured or the relay refused it): accept_url is then
	// set, for the admin to pass on by hand.
	EmailSent bool   `json:"emailSent" required:"false"`
	AcceptUrl string `json:"acceptUrl" required:"false"`
}

type UpdateUserRequest struct {
	Id          string `json:"id" required:"false"`
	DisplayName string `json:"displayName" required:"false"`
}

type ResetPasswordRequest struct {
	Id       string `json:"id" required:"false"`
	Password string `json:"password" required:"false"`
}

type RoleRequest struct {
	Id   string `json:"id" required:"false"`
	Role string `json:"role" required:"false"`
	// Both set: assign or revoke the role only on this scope. Both empty: the tenant-wide role.
	ScopeType string `json:"scopeType" required:"false"`
	ScopeId   string `json:"scopeId" required:"false"`
}

type ScopeRequest struct {
	ScopeType string `json:"scopeType" required:"false"`
	ScopeId   string `json:"scopeId" required:"false"`
}

type ScopeMember struct {
	UserId      string `json:"userId" required:"false"`
	Email       string `json:"email" required:"false"`
	DisplayName string `json:"displayName" required:"false"`
	Role        string `json:"role" required:"false"`
}

type ListScopeMembersResponse struct {
	Members []*ScopeMember `json:"members" required:"false"`
}

type ListUserAuditRequest struct {
	Id     string `json:"id" required:"false"`
	Cursor string `json:"cursor" required:"false"`
	Limit  int32  `json:"limit" required:"false"`
}

type CreateServiceAccountRequest struct {
	Name  string   `json:"name" required:"false"`
	Roles []string `json:"roles" required:"false"`
}
