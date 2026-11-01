package grpcapi

import (
	"testing"

	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/runtime"
)

func TestPickTokenPrefersTheForwardedTokenWhenPresent(t *testing.T) {
	rc := &permission.RequestContext{TenantID: "t1", UserID: "u1", ForwardedBearerToken: "user-token"}
	serviceToken := runtime.NewStaticServiceTokenSource("service-jwt")
	if got := pickToken(rc, serviceToken); got != "user-token" {
		t.Fatalf("got %q", got)
	}
}

func TestPickTokenFallsBackToTheServiceJWTWhenAbsent(t *testing.T) {
	rc := &permission.RequestContext{TenantID: "t1", UserID: "u1"}
	serviceToken := runtime.NewStaticServiceTokenSource("service-jwt")
	if got := pickToken(rc, serviceToken); got != "service-jwt" {
		t.Fatalf("got %q", got)
	}
}
