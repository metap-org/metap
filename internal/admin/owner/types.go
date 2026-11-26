package owner

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

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

func (x *TenantIdOnlyRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
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

func (x *TransferOwnershipRequest) GetMemberId() string {
	if x != nil {
		return x.MemberId
	}
	return ""
}
