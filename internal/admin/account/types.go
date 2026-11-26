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

func (x *ListTenantUsersResponse) GetUsers() []*TenantUser {
	if x != nil {
		return x.Users
	}
	return nil
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

func (x *ListMyTenantsResponse) GetTenants() []*MyTenant {
	if x != nil {
		return x.Tenants
	}
	return nil
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

func (x *UpdateProfileRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
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
