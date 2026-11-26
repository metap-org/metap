// Request and response types of the admin services. Generated once from the gRPC API's
// protobuf messages when it was removed (roadmap 116); hand-maintained from here on.

package admin

import (
	"github.com/metap-org/metap/internal/admin/kernel"
)

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

type Me struct {
	UserId      string   `json:"userId" required:"false"`
	TenantId    string   `json:"tenantId" required:"false"`
	Email       string   `json:"email" required:"false"`
	DisplayName string   `json:"displayName" required:"false"`
	Roles       []string `json:"roles" required:"false"`
}

type UpdateProfileRequest struct {
	DisplayName string `json:"displayName" required:"false"`
}

type MyTenant struct {
	TenantId string `json:"tenantId" required:"false"`
	Name     string `json:"name" required:"false"`
	Current  bool   `json:"current" required:"false"`
}

type ListMyTenantsResponse struct {
	Tenants []*MyTenant `json:"tenants" required:"false"`
}

type ListTenantUsersRequest struct {
	// Case-insensitive substring of email or display name; "" lists everyone.
	Search string `json:"search" required:"false"`
	// 0 = every active member.
	Limit int32 `json:"limit" required:"false"`
}

type TenantUser struct {
	Id          string `json:"id" required:"false"`
	Email       string `json:"email" required:"false"`
	DisplayName string `json:"displayName" required:"false"`
}

type ListTenantUsersResponse struct {
	Users []*TenantUser `json:"users" required:"false"`
}

type RequestWorkspaceDeletionRequest struct {
	// Must equal the workspace's name: a deliberate act, not a stray click.
	ConfirmName string `json:"confirmName" required:"false"`
	// The owner's current password: deleting a workspace cannot be undone from a stolen session.
	Password string `json:"password" required:"false"`
	// A second-factor (or recovery) code, required when the owner has one set up.
	MfaCode string `json:"mfaCode" required:"false"`
}

type WorkspaceDeletion struct {
	// When the workspace is deleted for good unless the deletion is cancelled first.
	EffectiveAt string `json:"effectiveAt" required:"false"`
	// The link that cancels the deletion, returned only when no email could be sent: otherwise it
	// exists only in the emails.
	CancelLink string `json:"cancelLink,omitempty" required:"false"`
}

type TransferOwnershipRequest struct {
	MemberId string `json:"memberId" required:"false"`
}

type RequestOwnerRecoveryRequest struct {
	TenantId string `json:"tenantId" required:"false"`
	// Becomes admin (if not already) and owner when the recovery completes.
	NewOwnerMemberId string `json:"newOwnerMemberId" required:"false"`
	Reason           string `json:"reason" required:"false"`
}

type TenantIdOnlyRequest struct {
	TenantId string `json:"tenantId" required:"false"`
}

type OwnerRecovery struct {
	Id               string `json:"id" required:"false"`
	NewOwnerMemberId string `json:"newOwnerMemberId" required:"false"`
	RequestedByEmail string `json:"requestedByEmail" required:"false"`
	Reason           string `json:"reason" required:"false"`
	EffectiveAt      string `json:"effectiveAt" required:"false"`
	CreatedAt        string `json:"createdAt" required:"false"`
	CompletedAt      string `json:"completedAt" required:"false"`
}

// TenantPerson is an active person in a tenant: who a platform admin can name as the new owner.
type TenantPerson struct {
	Id          string `json:"id" required:"false"`
	Email       string `json:"email" required:"false"`
	DisplayName string `json:"displayName" required:"false"`
	IsOwner     bool   `json:"isOwner" required:"false"`
}

// TenantOwnerView is what a platform admin sees before starting or finishing an owner recovery.
type TenantOwnerView struct {
	Owner  *OwnerStatus    `json:"owner" required:"false"`
	People []*TenantPerson `json:"people" required:"false"`
}

type OwnerStatus struct {
	OwnerMemberId string `json:"ownerMemberId" required:"false"`
	OwnerEmail    string `json:"ownerEmail" required:"false"`
	// Set while a recovery is pending.
	PendingRecovery *OwnerRecovery `json:"pendingRecovery" required:"false"`
}

type SetSsoConfigRequest struct {
	IssuerUrl string `json:"issuerUrl" required:"false"`
	ClientId  string `json:"clientId" required:"false"`
	// Empty keeps the stored secret (required on first setup).
	ClientSecret string `json:"clientSecret" required:"false"`
	// Default: openid, email, profile.
	Scopes []string `json:"scopes" required:"false"`
	// Only these email domains may sign in; empty = any.
	EmailDomains []string `json:"emailDomains" required:"false"`
	// Role a person provisioned on first sign-in starts with; never admin.
	DefaultRole string `json:"defaultRole" required:"false"`
	Enabled     bool   `json:"enabled" required:"false"`
	Enforced    bool   `json:"enforced" required:"false"`
	// Where the browser lands after sign-in (absolute URL); default: the portal root.
	PostLoginRedirect string `json:"postLoginRedirect" required:"false"`
	// The id_token claim that carries groups or roles, and which tenant role each value means.
	// Only the roles named in roleMap follow the provider; never admin.
	RoleClaim string            `json:"roleClaim" required:"false"`
	RoleMap   map[string]string `json:"roleMap" required:"false"`
}

type SsoConfig struct {
	Configured        bool     `json:"configured" required:"false"`
	Enabled           bool     `json:"enabled" required:"false"`
	IssuerUrl         string   `json:"issuerUrl" required:"false"`
	ClientId          string   `json:"clientId" required:"false"`
	HasClientSecret   bool     `json:"hasClientSecret" required:"false"`
	Scopes            []string `json:"scopes" required:"false"`
	EmailDomains      []string `json:"emailDomains" required:"false"`
	DefaultRole       string   `json:"defaultRole" required:"false"`
	Enforced          bool     `json:"enforced" required:"false"`
	PostLoginRedirect string   `json:"postLoginRedirect" required:"false"`
	RoleClaim         string   `json:"roleClaim" required:"false"`
	// Never null.
	RoleMap map[string]string `json:"roleMap" required:"false"`
	// Register this redirect URI at the identity provider.
	RedirectUri string `json:"redirectUri" required:"false"`
	// Every email domain named above and whether the workspace has proved it owns it. Only a
	// verified domain routes a sign-in here or lets an address match or create a member.
	Domains []*SsoDomain `json:"domains" required:"false"`
}

type SsoDomain struct {
	Domain string `json:"domain" required:"false"`
	// "pending" or "verified".
	Status string `json:"status" required:"false"`
	// Publish verifyValue as a TXT record at verifyHost, then ask for a check.
	VerifyHost  string `json:"verifyHost" required:"false"`
	VerifyValue string `json:"verifyValue" required:"false"`
}

// SetSamlConfigRequest sets up SAML single sign-on. Give the provider's metadata as a URL (fetched
// now) or as XML; give neither to keep the metadata already stored.
type SetSamlConfigRequest struct {
	IdpMetadataUrl string `json:"idpMetadataUrl" required:"false"`
	IdpMetadataXml string `json:"idpMetadataXml" required:"false"`
	// The attribute that carries the address; empty uses the NameID when it is an address.
	EmailAttribute string `json:"emailAttribute" required:"false"`
	// "emailAddress" (default) or "persistent". With persistent the provider's opaque identifier is
	// the person's identity, so a changed or reused address does not move the account; the address
	// then has to come from an attribute.
	NameIdFormat string `json:"nameIdFormat" required:"false"`
	// The attribute that lists groups or roles, and which tenant role each value means. Never admin.
	RoleAttribute     string            `json:"roleAttribute" required:"false"`
	RoleMap           map[string]string `json:"roleMap" required:"false"`
	EmailDomains      []string          `json:"emailDomains" required:"false"`
	DefaultRole       string            `json:"defaultRole" required:"false"`
	Enabled           bool              `json:"enabled" required:"false"`
	Enforced          bool              `json:"enforced" required:"false"`
	PostLoginRedirect string            `json:"postLoginRedirect" required:"false"`
}

type SamlConfig struct {
	Configured     bool   `json:"configured" required:"false"`
	Enabled        bool   `json:"enabled" required:"false"`
	IdpMetadataUrl string `json:"idpMetadataUrl" required:"false"`
	IdpEntityId    string `json:"idpEntityId" required:"false"`
	// The provider's sign-in endpoint taken from its metadata.
	IdpSsoUrl         string            `json:"idpSsoUrl" required:"false"`
	EmailAttribute    string            `json:"emailAttribute" required:"false"`
	NameIdFormat      string            `json:"nameIdFormat" required:"false"`
	RoleAttribute     string            `json:"roleAttribute" required:"false"`
	RoleMap           map[string]string `json:"roleMap" required:"false"`
	EmailDomains      []string          `json:"emailDomains" required:"false"`
	DefaultRole       string            `json:"defaultRole" required:"false"`
	Enforced          bool              `json:"enforced" required:"false"`
	PostLoginRedirect string            `json:"postLoginRedirect" required:"false"`
	// Register these at the identity provider.
	EntityId    string `json:"entityId" required:"false"`
	AcsUrl      string `json:"acsUrl" required:"false"`
	MetadataUrl string `json:"metadataUrl" required:"false"`
	// Each domain above and whether the workspace has proved it owns it.
	Domains []*SsoDomain `json:"domains" required:"false"`
}

type VerifySsoDomainRequest struct {
	Domain string `json:"domain" required:"false"`
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

func (x *UserIdRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
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

func (x *ListScopeMembersResponse) GetMembers() []*ScopeMember {
	if x != nil {
		return x.Members
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

func (x *Me) GetUserId() string {
	if x != nil {
		return x.UserId
	}
	return ""
}

func (x *Me) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *Me) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *Me) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *Me) GetRoles() []string {
	if x != nil {
		return x.Roles
	}
	return nil
}

func (x *UpdateProfileRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *MyTenant) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *MyTenant) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *MyTenant) GetCurrent() bool {
	if x != nil {
		return x.Current
	}
	return false
}

func (x *ListMyTenantsResponse) GetTenants() []*MyTenant {
	if x != nil {
		return x.Tenants
	}
	return nil
}

func (x *ListTenantUsersRequest) GetSearch() string {
	if x != nil {
		return x.Search
	}
	return ""
}

func (x *ListTenantUsersRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

func (x *TenantUser) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *TenantUser) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *TenantUser) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *ListTenantUsersResponse) GetUsers() []*TenantUser {
	if x != nil {
		return x.Users
	}
	return nil
}

func (x *TransferOwnershipRequest) GetMemberId() string {
	if x != nil {
		return x.MemberId
	}
	return ""
}

func (x *RequestOwnerRecoveryRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *RequestOwnerRecoveryRequest) GetNewOwnerMemberId() string {
	if x != nil {
		return x.NewOwnerMemberId
	}
	return ""
}

func (x *RequestOwnerRecoveryRequest) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *TenantIdOnlyRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *OwnerRecovery) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *OwnerRecovery) GetNewOwnerMemberId() string {
	if x != nil {
		return x.NewOwnerMemberId
	}
	return ""
}

func (x *OwnerRecovery) GetRequestedByEmail() string {
	if x != nil {
		return x.RequestedByEmail
	}
	return ""
}

func (x *OwnerRecovery) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *OwnerRecovery) GetEffectiveAt() string {
	if x != nil {
		return x.EffectiveAt
	}
	return ""
}

func (x *OwnerRecovery) GetCreatedAt() string {
	if x != nil {
		return x.CreatedAt
	}
	return ""
}

func (x *OwnerRecovery) GetCompletedAt() string {
	if x != nil {
		return x.CompletedAt
	}
	return ""
}

func (x *OwnerStatus) GetOwnerMemberId() string {
	if x != nil {
		return x.OwnerMemberId
	}
	return ""
}

func (x *OwnerStatus) GetOwnerEmail() string {
	if x != nil {
		return x.OwnerEmail
	}
	return ""
}

func (x *OwnerStatus) GetPendingRecovery() *OwnerRecovery {
	if x != nil {
		return x.PendingRecovery
	}
	return nil
}

func (x *SetSsoConfigRequest) GetIssuerUrl() string {
	if x != nil {
		return x.IssuerUrl
	}
	return ""
}

func (x *SetSsoConfigRequest) GetClientId() string {
	if x != nil {
		return x.ClientId
	}
	return ""
}

func (x *SetSsoConfigRequest) GetClientSecret() string {
	if x != nil {
		return x.ClientSecret
	}
	return ""
}

func (x *SetSsoConfigRequest) GetScopes() []string {
	if x != nil {
		return x.Scopes
	}
	return nil
}

func (x *SetSsoConfigRequest) GetEmailDomains() []string {
	if x != nil {
		return x.EmailDomains
	}
	return nil
}

func (x *SetSsoConfigRequest) GetDefaultRole() string {
	if x != nil {
		return x.DefaultRole
	}
	return ""
}

func (x *SetSsoConfigRequest) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

func (x *SetSsoConfigRequest) GetEnforced() bool {
	if x != nil {
		return x.Enforced
	}
	return false
}

func (x *SetSsoConfigRequest) GetPostLoginRedirect() string {
	if x != nil {
		return x.PostLoginRedirect
	}
	return ""
}

func (x *SsoConfig) GetConfigured() bool {
	if x != nil {
		return x.Configured
	}
	return false
}

func (x *SsoConfig) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

func (x *SsoConfig) GetIssuerUrl() string {
	if x != nil {
		return x.IssuerUrl
	}
	return ""
}

func (x *SsoConfig) GetClientId() string {
	if x != nil {
		return x.ClientId
	}
	return ""
}

func (x *SsoConfig) GetHasClientSecret() bool {
	if x != nil {
		return x.HasClientSecret
	}
	return false
}

func (x *SsoConfig) GetScopes() []string {
	if x != nil {
		return x.Scopes
	}
	return nil
}

func (x *SsoConfig) GetEmailDomains() []string {
	if x != nil {
		return x.EmailDomains
	}
	return nil
}

func (x *SsoConfig) GetDefaultRole() string {
	if x != nil {
		return x.DefaultRole
	}
	return ""
}

func (x *SsoConfig) GetEnforced() bool {
	if x != nil {
		return x.Enforced
	}
	return false
}

func (x *SsoConfig) GetPostLoginRedirect() string {
	if x != nil {
		return x.PostLoginRedirect
	}
	return ""
}

func (x *SsoConfig) GetRedirectUri() string {
	if x != nil {
		return x.RedirectUri
	}
	return ""
}

func (x *TenantPerson) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *TenantPerson) GetEmail() string {
	if x != nil {
		return x.Email
	}
	return ""
}

func (x *TenantPerson) GetIsOwner() bool {
	if x != nil {
		return x.IsOwner
	}
	return false
}

func (x *TenantOwnerView) GetOwner() *OwnerStatus {
	if x != nil {
		return x.Owner
	}
	return nil
}

func (x *TenantOwnerView) GetPeople() []*TenantPerson {
	if x != nil {
		return x.People
	}
	return nil
}

func (x *SetSsoConfigRequest) GetRoleClaim() string {
	if x != nil {
		return x.RoleClaim
	}
	return ""
}

func (x *SetSsoConfigRequest) GetRoleMap() map[string]string {
	if x != nil {
		return x.RoleMap
	}
	return nil
}

func (x *VerifySsoDomainRequest) GetDomain() string {
	if x != nil {
		return x.Domain
	}
	return ""
}

func (x *SetSamlConfigRequest) GetIdpMetadataUrl() string {
	if x != nil {
		return x.IdpMetadataUrl
	}
	return ""
}

func (x *SetSamlConfigRequest) GetIdpMetadataXml() string {
	if x != nil {
		return x.IdpMetadataXml
	}
	return ""
}

func (x *SetSamlConfigRequest) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

// The audit page the user and role histories share lives in kernel (the roles slice cannot import admin).
type (
	AuditEvent             = kernel.AuditEvent
	CredentialCapabilities = kernel.CredentialCapabilities
	CredentialManager      = kernel.CredentialManager
	Empty                  = kernel.Empty
	ListUserAuditResponse  = kernel.ListUserAuditResponse
)
