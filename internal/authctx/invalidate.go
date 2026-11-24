package authctx

import (
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// SubscribeChanges makes a role or policy change made through rolestore in this process show at once in
// the caches that hold a copy of it (the roles of a member, the tenant's policy matrix), instead of
// after their time to live. A change made by another process still takes up to the time to live to
// arrive. The returned function unsubscribes.
func SubscribeChanges(roles *RolesCache, policies permission.PolicyStore) func() {
	matrix, _ := policies.(*permission.MatrixStore)
	return rolestore.Subscribe(func(c rolestore.Change) {
		switch {
		case c.User != uuid.Nil:
			roles.Invalidate(c.Tenant, c.User)
		case !c.Policies:
			roles.InvalidateTenant(c.Tenant)
		}
		if c.Policies && matrix != nil {
			matrix.InvalidateTenant(c.Tenant)
		}
	})
}
