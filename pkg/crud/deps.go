package crud

//go:generate go run github.com/vektra/mockery/v3@v3.8.1 --config ../../.mockery.yml

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/permission"
)

// TxBeginner opens a transaction routed to a tenant's own database/schema. *control.Router is
// the production implementation; tests substitute a mock (see mocks_test.go) to exercise every
// decision CrudService makes before it touches the database.
type TxBeginner interface {
	Begin(ctx context.Context, tenantID uuid.UUID) (pgx.Tx, error)
}

// PermissionChecker is the slice of *permission.PermissionService CrudService depends on.
type PermissionChecker interface {
	ScopedTenant(rc *permission.RequestContext) (uuid.UUID, error)
	CanReadEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanCreateEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanUpdateEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanDeleteEntity(ctx context.Context, rc *permission.RequestContext, entity string) (permission.PermissionDecision, error)
	CanTransitionAction(ctx context.Context, rc *permission.RequestContext, entity, action string) (permission.PermissionDecision, error)
	LoadSnapshot(ctx context.Context, tenantID uuid.UUID, entity string) (*permission.PermissionSnapshot, error)
}

var (
	_ TxBeginner        = (*control.Router)(nil)
	_ PermissionChecker = (*permission.PermissionService)(nil)
)
