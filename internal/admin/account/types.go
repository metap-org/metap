package account

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

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
