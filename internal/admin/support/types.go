package support

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type SupportGrant struct {
	Id        string                `json:"id" required:"false"`
	Reason    string                `json:"reason" required:"false"`
	Role      string                `json:"role" required:"false"`
	GrantedBy string                `json:"grantedBy" required:"false"`
	ExpiresAt string                `json:"expiresAt" required:"false"`
	RevokedAt string                `json:"revokedAt" required:"false"`
	CreatedAt string                `json:"createdAt" required:"false"`
	Active    bool                  `json:"active" required:"false"`
	Sessions  []*SupportSessionInfo `json:"sessions" required:"false"`
}

type SupportSessionInfo struct {
	MemberId       string `json:"memberId" required:"false"`
	StartedByEmail string `json:"startedByEmail" required:"false"`
	StartedAt      string `json:"startedAt" required:"false"`
}

type GrantSupportAccessRequest struct {
	Reason string `json:"reason" required:"false"`
	// 1..168, default 24.
	Hours int32 `json:"hours" required:"false"`
	// The role support will hold in the tenant; it must exist there.
	Role string `json:"role" required:"false"`
}

type ListSupportGrantsResponse struct {
	Grants []*SupportGrant `json:"grants" required:"false"`
}

type SupportGrantIdRequest struct {
	Id string `json:"id" required:"false"`
}

type StartSupportSessionRequest struct {
	TenantId string `json:"tenantId" required:"false"`
	// Optional: the grant to use; default is the tenant's newest active one.
	GrantId string `json:"grantId" required:"false"`
}

type SupportSessionToken struct {
	// An API key (mk_...), shown once.
	Token     string `json:"token" required:"false"`
	ExpiresAt string `json:"expiresAt" required:"false"`
	TenantId  string `json:"tenantId" required:"false"`
	Role      string `json:"role" required:"false"`
	GrantId   string `json:"grantId" required:"false"`
}
