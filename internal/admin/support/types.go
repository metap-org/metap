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

func (x *SupportSessionToken) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *SupportSessionToken) GetExpiresAt() string {
	if x != nil {
		return x.ExpiresAt
	}
	return ""
}

func (x *SupportSessionToken) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *SupportSessionToken) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *SupportSessionToken) GetGrantId() string {
	if x != nil {
		return x.GrantId
	}
	return ""
}

func (x *StartSupportSessionRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

func (x *StartSupportSessionRequest) GetGrantId() string {
	if x != nil {
		return x.GrantId
	}
	return ""
}

func (x *SupportGrantIdRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ListSupportGrantsResponse) GetGrants() []*SupportGrant {
	if x != nil {
		return x.Grants
	}
	return nil
}

func (x *GrantSupportAccessRequest) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *GrantSupportAccessRequest) GetHours() int32 {
	if x != nil {
		return x.Hours
	}
	return 0
}

func (x *GrantSupportAccessRequest) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *SupportSessionInfo) GetMemberId() string {
	if x != nil {
		return x.MemberId
	}
	return ""
}

func (x *SupportSessionInfo) GetStartedByEmail() string {
	if x != nil {
		return x.StartedByEmail
	}
	return ""
}

func (x *SupportSessionInfo) GetStartedAt() string {
	if x != nil {
		return x.StartedAt
	}
	return ""
}

func (x *SupportGrant) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *SupportGrant) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *SupportGrant) GetRole() string {
	if x != nil {
		return x.Role
	}
	return ""
}

func (x *SupportGrant) GetGrantedBy() string {
	if x != nil {
		return x.GrantedBy
	}
	return ""
}

func (x *SupportGrant) GetExpiresAt() string {
	if x != nil {
		return x.ExpiresAt
	}
	return ""
}

func (x *SupportGrant) GetRevokedAt() string {
	if x != nil {
		return x.RevokedAt
	}
	return ""
}

func (x *SupportGrant) GetCreatedAt() string {
	if x != nil {
		return x.CreatedAt
	}
	return ""
}

func (x *SupportGrant) GetActive() bool {
	if x != nil {
		return x.Active
	}
	return false
}

func (x *SupportGrant) GetSessions() []*SupportSessionInfo {
	if x != nil {
		return x.Sessions
	}
	return nil
}
