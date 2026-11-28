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
