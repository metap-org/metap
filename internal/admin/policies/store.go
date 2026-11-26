package policies

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
)

// Store is a tenant's entity policies. permission.PermissionService is the Postgres-backed
// implementation (PgStore): it also keeps the service's policy cache in step with every write.
// Writes run on the transaction of the unit of work in ctx when there is one, so a change and its
// audit entry are one transaction on the tenant's database.
type Store interface {
	List(ctx context.Context, tenant uuid.UUID, entity string, hasEntity bool) ([]permission.PolicyRow, error)
	Create(ctx context.Context, tenant uuid.UUID, in NewPolicy) (permission.PolicyRow, error)
	// Sync replaces every basic-shaped policy of the entity with exactly grants.
	Sync(ctx context.Context, tenant uuid.UUID, entity string, grants []permission.RoleActionGrant, by *uuid.UUID) ([]permission.PolicyRow, error)
	Delete(ctx context.Context, tenant, id uuid.UUID) error
	// Explain says which policies decide an action on an entity for the caller.
	Explain(ctx context.Context, rc *permission.RequestContext, entity, action, field string, hasField bool,
		record permission.JsonObject, hasRecord bool) (permission.PolicyExplanation, error)
}

// NewPolicy is one policy to create.
type NewPolicy struct {
	Entity, Action string
	Roles          []string
	HasRoles       bool
	Condition      *permission.PolicyCondition
	By             *uuid.UUID
	Field          string
	Subject        permission.PolicySubject
	Effect         permission.PolicyEffect
}

// ContextInvalidator drops what is cached about a member's context attributes.
type ContextInvalidator interface {
	Invalidate(tenantID, userID uuid.UUID)
}
