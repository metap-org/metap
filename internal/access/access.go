// Package access is the one place that says who an administrative operation lets in: any signed-in
// caller, a tenant admin, or the platform's own admin (roadmap 120, P4).
//
// Five places used to spell this out: the HTTP gates, the helpers every admin service starts with,
// and the tenant service's own copy. They agreed by habit. Now they ask Refusal, so a caller is let
// in or refused for the same reason and with the same words wherever the question is asked.
//
// What needs the database (an admin who is a vendor's support session may not create people or
// keys) is not decided here: that is admin.Require, which asks Refusal first.
package access

import (
	"slices"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Level is who an operation lets in.
type Level int

const (
	// Authenticated: any signed-in caller (what the operation shows or changes is bounded by its own rules).
	Authenticated Level = iota
	// Admin: a tenant admin, holding the admin OAuth2 scope when the token is scoped.
	Admin
	// Durable: an admin who is not a vendor's support session. The operation creates access that
	// outlives the moment (a person, a key, a role, a password or MFA reset, a sign-in provider), so
	// revoking or lapsing a support grant must really end it. The support-session half needs the
	// database and is checked by admin.Require; Refusal covers the admin half.
	Durable
	// PlatformAdmin: the platform tenant's platform_admin role.
	PlatformAdmin
)

func (l Level) String() string {
	switch l {
	case Authenticated:
		return "authenticated"
	case Admin:
		return "admin"
	case Durable:
		return "durable"
	case PlatformAdmin:
		return "platformAdmin"
	}
	return "unknown"
}

// RolePlatformAdmin is the built-in role of the platform tenant.
const RolePlatformAdmin = "platform_admin"

// Refusal says why rc may not use an operation of level l, or "" when it may. It needs nothing but
// the caller, so it is the same on the route and inside a service.
func Refusal(l Level, rc *permission.RequestContext) string {
	switch l {
	case Admin, Durable:
		if !rc.IsAdmin() {
			return "This action requires the admin role."
		}
		if !rc.ScopePermitsAdminSurface() {
			return "This action requires the `admin` OAuth2 scope."
		}
	case PlatformAdmin:
		if rc.TenantID != tenancy.PlatformTenantID.String() || !slices.Contains(rc.Roles, RolePlatformAdmin) {
			return "This action requires the platform_admin role."
		}
		if !rc.ScopePermitsAdminSurface() {
			return "This action requires the `admin` OAuth2 scope."
		}
	}
	return ""
}
