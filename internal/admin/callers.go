package admin

import (
	"context"
	"net/http"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
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
	return callerAt(ctx, access.Admin)
}

// platformAdminCaller is the caller in ctx, who must hold platform_admin in the platform tenant.
func platformAdminCaller(ctx context.Context) (*permission.RequestContext, error) {
	return callerAt(ctx, access.PlatformAdmin)
}

// callerAt is the caller in ctx when level lets them in.
func callerAt(ctx context.Context, level access.Level) (*permission.RequestContext, error) {
	rc, err := authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	if why := access.Refusal(level, rc); why != "" {
		return nil, svcErr(ctx, http.StatusForbidden, errcode.Forbidden, why)
	}
	return rc, nil
}
