package grpcapi

import (
	"testing"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestPickTokenPrefersTheForwardedTokenWhenPresent(t *testing.T) {
	rc := &permission.RequestContext{TenantID: "t1", UserID: "u1", ForwardedBearerToken: "user-token"}
	serviceToken := runtime.NewStaticServiceTokenSource("service-jwt")
	got := pickToken(rc, serviceToken)
	require.Equal(t, "user-token", got, "got %q", got)
}

func TestPickTokenFallsBackToTheServiceJWTWhenAbsent(t *testing.T) {
	rc := &permission.RequestContext{TenantID: "t1", UserID: "u1"}
	serviceToken := runtime.NewStaticServiceTokenSource("service-jwt")
	got := pickToken(rc, serviceToken)
	require.Equal(t, "service-jwt", got, "got %q", got)
}
