package admin

import (
	"context"
	"net/http"
	"slices"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// authenticatedCaller is the caller in ctx; any signed-in member will do.
func authenticatedCaller(ctx context.Context) (*permission.RequestContext, error) {
	rc, err := resolveCaller(ctx)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, svcErr(ctx, http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid authorization header.")
	}
	return rc, nil
}

// adminCaller is the caller in ctx, who must hold the admin role (and the admin OAuth2 scope).
func adminCaller(ctx context.Context) (*permission.RequestContext, error) {
	rc, err := authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !rc.IsAdmin() {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the admin role")
	}
	if !rc.ScopePermitsAdminSurface() {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the `admin` OAuth2 scope")
	}
	return rc, nil
}

// platformAdminCaller is the caller in ctx, who must hold platform_admin in the platform tenant.
func platformAdminCaller(ctx context.Context) (*permission.RequestContext, error) {
	rc, err := authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	if rc.TenantID != tenancy.PlatformTenantID.String() || !slices.Contains(rc.Roles, "platform_admin") {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the platform_admin role")
	}
	if !rc.ScopePermitsAdminSurface() {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, "this action requires the `admin` OAuth2 scope")
	}
	return rc, nil
}
