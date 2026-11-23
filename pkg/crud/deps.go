package crud

//go:generate go run github.com/vektra/mockery/v3@v3.8.1 --config ../../.mockery.yml

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// TxBeginner opens a transaction routed to a tenant's own database/schema. *tenancy.Router is
// the production implementation; tests substitute a mock (see mocks_test.go) to exercise every
// decision CrudService makes before it touches the database.
type TxBeginner interface {
	Begin(ctx context.Context, tenantID uuid.UUID) (pgx.Tx, error)
}

// EntityAccess answers the entity-level question: may this caller's roles do this to this entity
// at all (the tenant's policies, with no record in view). *permission.PermissionService is the
// production implementation; with a MatrixStore behind it the answer comes from memory.
type EntityAccess interface {
	CanReadEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanCreateEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanUpdateEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanDeleteEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanTransitionAction(ctx context.Context, rc *permission.RequestContext, entity, action string) (permission.PermissionDecision, error)
}

// PermissionChecker is the slice of *permission.PermissionService CrudService depends on: the
// entity-level question (EntityAccess) plus what record-level decisions need.
type PermissionChecker interface {
	EntityAccess
	ScopedTenant(rc *permission.RequestContext) (uuid.UUID, error)
	LoadSnapshot(ctx context.Context, tenantID uuid.UUID, entity string) (*permission.PermissionSnapshot, error)
}

var (
	_ TxBeginner        = (*tenancy.Router)(nil)
	_ PermissionChecker = (*permission.PermissionService)(nil)
)
