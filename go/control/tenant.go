// Package control is the SaaS multi-tenancy control plane — ported from metap's Rust
// metap-control crate: control.tenants registry, Router (opens every tenant-scoped transaction),
// SecretStore (pluggable tenant-credential resolution), tenant provisioning, per-tenant schema
// isolation, and the PolicyStore/RequestContext glue that needs Router to reach a tenant's own
// database. No HTTP, no business-entity knowledge — a plain library, same shape as permission.
package control

import "github.com/google/uuid"

// PlatformTenantID is the sentinel tenant a platform-superadmin's JWT carries as tenantId — not
// a real tenant and never routed to by Router.Begin; it exists purely so users/user_roles
// (always public, never Router-scoped) has somewhere to hold a platform-admin's identity.
// uuid.Nil (all-zero) — verified unused by any other reserved id in this codebase.
var PlatformTenantID = uuid.Nil

// TenantStatus mirrors control.tenants.status.
type TenantStatus int

const (
	StatusProvisioning TenantStatus = iota
	StatusActive
	StatusMigrating
	StatusSuspended
	StatusExpired
	// StatusDeleted is terminal, never reversible. Deliberately does not drop the tenant's
	// physical data — see this type's Rust doc comment for why. Only ever changes what
	// Router.Begin does: refuse permanently.
	StatusDeleted
)

func (s TenantStatus) String() string {
	switch s {
	case StatusProvisioning:
		return "provisioning"
	case StatusActive:
		return "active"
	case StatusMigrating:
		return "migrating"
	case StatusSuspended:
		return "suspended"
	case StatusExpired:
		return "expired"
	default:
		return "deleted"
	}
}

func ParseTenantStatus(s string) (TenantStatus, error) {
	switch s {
	case "provisioning":
		return StatusProvisioning, nil
	case "active":
		return StatusActive, nil
	case "migrating":
		return StatusMigrating, nil
	case "suspended":
		return StatusSuspended, nil
	case "expired":
		return StatusExpired, nil
	case "deleted":
		return StatusDeleted, nil
	default:
		return 0, unknownStatusError(s)
	}
}

type unknownStatusError string

func (e unknownStatusError) Error() string {
	return "unknown control.tenants.status value: " + string(e)
}

// TenantStrategyKind discriminates TenantStrategy's two shapes.
type TenantStrategyKind int

const (
	StrategySchema TenantStrategyKind = iota
	StrategyDedicatedDb
)

// TenantStrategy mirrors control.tenants.strategy plus whichever of schema_name/dsn_secret_ref
// that strategy uses.
type TenantStrategy struct {
	Kind         TenantStrategyKind
	SchemaName   string // set when Kind == StrategySchema
	DsnSecretRef string // set when Kind == StrategyDedicatedDb
}

func SchemaStrategy(schemaName string) TenantStrategy {
	return TenantStrategy{Kind: StrategySchema, SchemaName: schemaName}
}

func DedicatedDbStrategy(dsnSecretRef string) TenantStrategy {
	return TenantStrategy{Kind: StrategyDedicatedDb, DsnSecretRef: dsnSecretRef}
}

type TenantRouting struct {
	Status   TenantStatus
	Strategy TenantStrategy
}
