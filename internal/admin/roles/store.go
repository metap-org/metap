package roles

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// Store is a tenant's role catalog: the roles, the grants they carry and who holds them. Methods run
// on the transaction of the unit of work in ctx when there is one, so a change and its audit entry
// are one transaction. Errors are rolestore's (ErrRoleNotFound, ErrRoleExists), which the service maps.
// Every read creates the built-in roles on first use.
type Store interface {
	// Catalog is every role matching search with how many policies name each.
	Catalog(ctx context.Context, tenant uuid.UUID, search string) ([]rolestore.Role, map[string]int, error)
	// Get is one role and the policies naming it.
	Get(ctx context.Context, tenant uuid.UUID, name string) (rolestore.Role, []rolestore.PolicyRef, error)
	// ActiveHolders is, per role, how many active members hold it.
	ActiveHolders(ctx context.Context, tenant uuid.UUID, names []string) (map[string]int, error)
	// HolderEmails is the addresses of up to limit of the given members.
	HolderEmails(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID, limit int) ([]string, error)

	// Create adds a role, copying copyFrom's basic grants when that is not empty; it returns how many
	// grants it copied.
	Create(ctx context.Context, tenant uuid.UUID, name, label, description, copyFrom string, by *uuid.UUID) (rolestore.Role, int, error)
	// Update changes label and description and returns the role before and after.
	Update(ctx context.Context, tenant uuid.UUID, name, label, description string) (before, after rolestore.Role, err error)
	// Usage is the role with everyone and everything that still uses it.
	Usage(ctx context.Context, tenant uuid.UUID, name string) (rolestore.Role, []uuid.UUID, []rolestore.PolicyRef, error)
	Delete(ctx context.Context, tenant uuid.UUID, name string) error

	// Permissions is the role's basic grants and the policies that go beyond them.
	Permissions(ctx context.Context, tenant uuid.UUID, name string) ([]rolestore.RoleGrant, []rolestore.AdvancedPolicy, error)
	// ReplaceGrants makes grants the role's complete set and returns the role and the set it had.
	ReplaceGrants(ctx context.Context, tenant uuid.UUID, name string, grants []rolestore.RoleGrant, by *uuid.UUID) (rolestore.Role, []rolestore.RoleGrant, error)
	// Role is just the catalog row (the audit history needs its id).
	Role(ctx context.Context, tenant uuid.UUID, name string) (rolestore.Role, error)
}
