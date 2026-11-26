package admin

import (
	"context"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntityUsers is the audit-trail entity name user administration records under.
const AuditEntityUsers = kernel.AuditEntityUsers

// RolePlatformAdmin is the built-in role of the platform tenant.
const RolePlatformAdmin = access.RolePlatformAdmin

// IdentityConfig is what the administrative slices are built from (kernel.Config).
type IdentityConfig = kernel.Config

// SupportSessionRefusal says why rc may not use an access.Durable operation because it is a vendor's
// support session ("" when it may); see kernel.SupportSessionRefusal.
func SupportSessionRefusal(ctx context.Context, cfg IdentityConfig, rc *permission.RequestContext) (string, error) {
	return kernel.SupportSessionRefusal(ctx, cfg, rc)
}
