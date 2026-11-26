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

func (x *CreateServiceAccountRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *CreateServiceAccountRequest) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *ListUserAuditRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ListUserAuditRequest) GetCursor() string {
	if x != nil {
		return x.Cursor
	}
	return ""
}

func (x *ListUserAuditRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

func (x *ListScopeMembersResponse) GetMembers() []*ScopeMember {
	if x != nil {
		return x.Members
	}
	return nil
}

func (x *ScopeMember) GetUserId() string {
	if x != nil {
		return x.UserId
	}
	return ""
}

func (x *ScopeMember) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *ScopeMember) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *ScopeMember) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *ScopeRequest) GetScopeType() string {
	if x != nil {
		return x.ScopeType
	}
	return ""
}

func (x *ScopeRequest) GetScopeId() string {
	if x != nil {
		return x.ScopeId
	}
	return ""
}

func (x *RoleRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *RoleRequest) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *RoleRequest) GetScopeType() string {
	if x != nil {
		return x.ScopeType
	}
	return ""
}

func (x *RoleRequest) GetScopeId() string {
	if x != nil {
		return x.ScopeId
	}
	return ""
}

func (x *ResetPasswordRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ResetPasswordRequest) GetPassword() string {
	if x != nil {
		return x.Password
	}
	return ""
}

func (x *UpdateUserRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *UpdateUserRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *InviteUserResponse) GetUser() *User {
	if x != nil {
		return x.User
	}
	return nil
}

func (x *InviteUserResponse) GetExpiresAt() string {
	if x != nil {
		return x.ExpiresAt
	}
	return ""
}

func (x *InviteUserResponse) GetEmailSent() bool {
	if x != nil {
		return x.EmailSent
	}
	return false
}

func (x *InviteUserResponse) GetAcceptUrl() string {
	if x != nil {
		return x.AcceptUrl
	}
	return ""
}

func (x *InviteUserRequest) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *InviteUserRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *InviteUserRequest) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *CreateUserResponse) GetUser() *User {
	if x != nil {
		return x.User
	}
	return nil
}

func (x *CreateUserResponse) GetCreatedIdentity() bool {
	if x != nil {
		return x.CreatedIdentity
	}
	return false
}

func (x *CreateUserRequest) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *CreateUserRequest) GetPassword() string {
	if x != nil {
		return x.Password
	}
	return ""
}

func (x *CreateUserRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *CreateUserRequest) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *UserIdRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ListUsersResponse) GetUsers() []*User {
	if x != nil {
		return x.Users
	}
	return nil
}

func (x *ListUsersResponse) GetTotal() int32 {
	if x != nil {
		return x.Total
	}
	return 0
}

func (x *ListUsersRequest) GetSearch() string {
	if x != nil {
		return x.Search
	}
	return ""
}

func (x *ListUsersRequest) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

func (x *ListUsersRequest) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *ListUsersRequest) GetOffset() int32 {
	if x != nil {
		return x.Offset
	}
	return 0
}

func (x *ListUsersRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

func (x *ScopedRole) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *ScopedRole) GetScopeType() string {
	if x != nil {
		return x.ScopeType
	}
	return ""
}

func (x *ScopedRole) GetScopeId() string {
	if x != nil {
		return x.ScopeId
	}
	return ""
}

func (x *User) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *User) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *User) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *User) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

func (x *User) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *User) GetAuthProvider() string {
	if x != nil {
		return x.AuthProvider
	}
	return ""
}

func (x *User) GetHasPassword() bool {
	if x != nil {
		return x.HasPassword
	}
	return false
}

func (x *User) GetLastLoginAt() string {
	if x != nil {
		return x.LastLoginAt
	}
	return ""
}

func (x *User) GetCreatedAt() string {
	if x != nil {
		return x.CreatedAt
	}
	return ""
}

func (x *User) GetInvitedBy() string {
	if x != nil {
		return x.InvitedBy
	}
	return ""
}

func (x *User) GetSharedIdentity() bool {
	if x != nil {
		return x.SharedIdentity
	}
	return false
}

func (x *User) GetInvitationExpiresAt() string {
	if x != nil {
		return x.InvitationExpiresAt
	}
	return ""
}

func (x *User) GetScopedRoles() []*ScopedRole {
	if x != nil {
		return x.ScopedRoles
	}
	return nil
}

func (x *User) GetIsOwner() bool {
	if x != nil {
		return x.IsOwner
	}
	return false
}
