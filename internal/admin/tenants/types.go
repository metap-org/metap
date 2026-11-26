package tenants

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type Tenant struct {
	Id   string `json:"id" required:"false"`
	Name string `json:"name" required:"false"`
	Tier string `json:"tier" required:"false"`
	// "schema" (shared tables or a schema of its own) or "dedicated_db".
	Strategy string `json:"strategy" required:"false"`
	// "provisioning", "active", "migrating", "suspended", "expired" or "deleted".
	Status         string `json:"status" required:"false"`
	Product        string `json:"product" required:"false"`
	CreatedAt      string `json:"createdAt" required:"false"`
	TrialExpiresAt string `json:"trialExpiresAt" required:"false"`
	// Both set while a deletion is pending ("" otherwise).
	DeletionRequestedAt string `json:"deletionRequestedAt" required:"false"`
	DeletionEffectiveAt string `json:"deletionEffectiveAt" required:"false"`
	MemberCount         int32  `json:"memberCount" required:"false"`
}

type ListTenantsRequest struct {
	// Case-insensitive substring of the name, or an exact tenant id.
	Query  string `json:"query" required:"false"`
	Status string `json:"status" required:"false"`
	Tier   string `json:"tier" required:"false"`
	// 0 = 50; at most 200.
	Limit  int32 `json:"limit" required:"false"`
	Offset int32 `json:"offset" required:"false"`
}

type ListTenantsResponse struct {
	Tenants []*Tenant `json:"tenants" required:"false"`
	Total   int32     `json:"total" required:"false"`
}

type TenantIdRequest struct {
	Id string `json:"id" required:"false"`
}

type CreateTenantRequest struct {
	Name string `json:"name" required:"false"`
	// "" = "trial".
	Tier             string `json:"tier" required:"false"`
	AdminEmail       string `json:"adminEmail" required:"false"`
	AdminDisplayName string `json:"adminDisplayName" required:"false"`
	Product          string `json:"product" required:"false"`
}

type CreateTenantResponse struct {
	Tenant        *Tenant `json:"tenant" required:"false"`
	AdminMemberId string  `json:"adminMemberId" required:"false"`
	ExpiresAt     string  `json:"expiresAt" required:"false"`
	EmailSent     bool    `json:"emailSent" required:"false"`
	// Set when the email could not be sent: the link to pass on by hand.
	AcceptUrl string `json:"acceptUrl" required:"false"`
}

type UpdateTenantRequest struct {
	Id   string  `json:"id" required:"false"`
	Name *string `json:"name,omitempty" required:"false"`
	Tier *string `json:"tier,omitempty" required:"false"`
}

type RequestTenantDeletionRequest struct {
	Id string `json:"id" required:"false"`
	// 0 = 30 days.
	RetentionDays int32 `json:"retentionDays" required:"false"`
}

func (x *RequestTenantDeletionRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *RequestTenantDeletionRequest) GetRetentionDays() int32 {
	if x != nil {
		return x.RetentionDays
	}
	return 0
}

func (x *UpdateTenantRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *UpdateTenantRequest) GetName() string {
	if x != nil && x.Name != nil {
		return *x.Name
	}
	return ""
}

func (x *UpdateTenantRequest) GetTier() string {
	if x != nil && x.Tier != nil {
		return *x.Tier
	}
	return ""
}

func (x *CreateTenantResponse) GetTenant() *Tenant {
	if x != nil {
		return x.Tenant
	}
	return nil
}

func (x *CreateTenantResponse) GetAdminMemberId() string {
	if x != nil {
		return x.AdminMemberId
	}
	return ""
}

func (x *CreateTenantResponse) GetExpiresAt() string {
	if x != nil {
		return x.ExpiresAt
	}
	return ""
}

func (x *CreateTenantResponse) GetEmailSent() bool {
	if x != nil {
		return x.EmailSent
	}
	return false
}

func (x *CreateTenantResponse) GetAcceptUrl() string {
	if x != nil {
		return x.AcceptUrl
	}
	return ""
}

func (x *CreateTenantRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *CreateTenantRequest) GetTier() string {
	if x != nil {
		return x.Tier
	}
	return ""
}

func (x *CreateTenantRequest) GetAdminEmail() string {
	if x != nil {
		return x.AdminEmail
	}
	return ""
}

func (x *CreateTenantRequest) GetAdminDisplayName() string {
	if x != nil {
		return x.AdminDisplayName
	}
	return ""
}

func (x *CreateTenantRequest) GetProduct() string {
	if x != nil {
		return x.Product
	}
	return ""
}

func (x *TenantIdRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ListTenantsResponse) GetTenants() []*Tenant {
	if x != nil {
		return x.Tenants
	}
	return nil
}

func (x *ListTenantsResponse) GetTotal() int32 {
	if x != nil {
		return x.Total
	}
	return 0
}

func (x *ListTenantsRequest) GetQuery() string {
	if x != nil {
		return x.Query
	}
	return ""
}

func (x *ListTenantsRequest) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

func (x *ListTenantsRequest) GetTier() string {
	if x != nil {
		return x.Tier
	}
	return ""
}

func (x *ListTenantsRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

func (x *ListTenantsRequest) GetOffset() int32 {
	if x != nil {
		return x.Offset
	}
	return 0
}

func (x *Tenant) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Tenant) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Tenant) GetTier() string {
	if x != nil {
		return x.Tier
	}
	return ""
}

func (x *Tenant) GetStrategy() string {
	if x != nil {
		return x.Strategy
	}
	return ""
}

func (x *Tenant) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

func (x *Tenant) GetProduct() string {
	if x != nil {
		return x.Product
	}
	return ""
}

func (x *Tenant) GetCreatedAt() string {
	if x != nil {
		return x.CreatedAt
	}
	return ""
}

func (x *Tenant) GetTrialExpiresAt() string {
	if x != nil {
		return x.TrialExpiresAt
	}
	return ""
}

func (x *Tenant) GetDeletionRequestedAt() string {
	if x != nil {
		return x.DeletionRequestedAt
	}
	return ""
}

func (x *Tenant) GetDeletionEffectiveAt() string {
	if x != nil {
		return x.DeletionEffectiveAt
	}
	return ""
}

func (x *Tenant) GetMemberCount() int32 {
	if x != nil {
		return x.MemberCount
	}
	return 0
}
