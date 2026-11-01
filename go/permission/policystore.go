package permission

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// PolicyEffect: Deny overrides Allow — see the Rust PolicyEffect's doc comment.
type PolicyEffect int

const (
	EffectAllow PolicyEffect = iota
	EffectDeny
)

func (e PolicyEffect) String() string {
	if e == EffectDeny {
		return "deny"
	}
	return "allow"
}

// MarshalJSON serializes as the lowercase string the Rust #[derive(Serialize)] PolicyEffect
// enum produces ("allow"/"deny") — found needed while porting PolicyTraceEntry/PolicyDto's own
// wire shape (this type has no explicit tag, so encoding/json would otherwise emit the
// underlying int unchanged).
func (e PolicyEffect) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

// ParsePolicyEffect: anything other than exactly "deny" parses as Allow.
func ParsePolicyEffect(s string) PolicyEffect {
	if s == "deny" {
		return EffectDeny
	}
	return EffectAllow
}

// PolicyRow mirrors the Rust PolicyRow.
type PolicyRow struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Entity    string
	Action    string
	Field     string // "" means absent
	Subject   string
	Roles     []string
	HasRoles  bool
	Condition *PolicyCondition
	CreatedBy *uuid.UUID
	Effect    PolicyEffect
}

// PolicySubject is "context" or "record".
type PolicySubject int

const (
	SubjectContext PolicySubject = iota
	SubjectRecord
)

func (s PolicySubject) String() string {
	if s == SubjectRecord {
		return "record"
	}
	return "context"
}

// ExplainOptions narrows PolicyStore.FindExplainPolicies.
type ExplainOptions struct {
	Field   string
	Subject *PolicySubject
}

// PolicyStore is the swappable storage interface — the real Postgres-backed implementation
// lives wherever tenant-DB routing lives (a later wave), same layering reason the Rust crate
// keeps PostgresPolicyStore out of this package.
type PolicyStore interface {
	FindContextPolicies(ctx context.Context, tenantID uuid.UUID, entity, action string) ([]PolicyRow, error)
	LoadAllPolicies(ctx context.Context, tenantID uuid.UUID, entity string) ([]PolicyRow, error)
	FindExplainPolicies(ctx context.Context, tenantID uuid.UUID, entity, action string, opts ExplainOptions) ([]PolicyRow, error)
	ListPolicies(ctx context.Context, tenantID uuid.UUID, entity string, hasEntity bool) ([]PolicyRow, error)
	CreatePolicy(ctx context.Context, tenantID uuid.UUID, entity, action string, roles []string, hasRoles bool,
		condition *PolicyCondition, createdBy *uuid.UUID, field string, subject *PolicySubject, effect PolicyEffect) (PolicyRow, error)
	DeletePolicy(ctx context.Context, tenantID, id uuid.UUID) error
	// SyncBasicPolicies: grants is the complete desired set of (role, action) pairs — role ""
	// means "Everyone" (Rust's role: None). See the Rust PolicyStore::sync_basic_policies doc
	// comment for exactly what "basic-shaped" means.
	SyncBasicPolicies(ctx context.Context, tenantID uuid.UUID, entity string, grants []RoleActionGrant, createdBy *uuid.UUID) ([]PolicyRow, error)
}

type RoleActionGrant struct {
	Role    string // "" means Everyone
	HasRole bool
	Action  string
}
