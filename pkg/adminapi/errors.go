// Package adminapi is metap's user, tenant and access administration (roadmap 113-115) as plain
// Go services: users, roles and permissions, invitations, the caller's own account (profile, MFA,
// sessions), API keys, tenants, audit search, support access, ownership, SSO and SCIM. Moved off
// gRPC (roadmap 116): requests and responses are plain structs (types.go), failures are
// *crud.ServiceError, and the caller is the RequestContext the HTTP layer authenticated
// (WithCaller). pkg/httpserver serves them as REST.
package adminapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type callerKey struct{}

// WithCaller is ctx carrying the authenticated caller the services act as.
func WithCaller(ctx context.Context, rc *permission.RequestContext) context.Context {
	return context.WithValue(ctx, callerKey{}, rc)
}

type authenticatorKey struct{}

// Authenticator resolves the caller when a service call needs it.
type Authenticator func(ctx context.Context) (*permission.RequestContext, error)

// WithAuthenticator is ctx whose caller is resolved by auth at call time rather than up front:
// a token revoked between two calls made with the same ctx fails the second one.
func WithAuthenticator(ctx context.Context, auth Authenticator) context.Context {
	return context.WithValue(ctx, authenticatorKey{}, auth)
}

// CallerFrom is the caller WithCaller put in ctx; nil when none.
func CallerFrom(ctx context.Context) *permission.RequestContext {
	rc, _ := ctx.Value(callerKey{}).(*permission.RequestContext)
	return rc
}

// resolveCaller is the caller in ctx (WithCaller, else WithAuthenticator).
func resolveCaller(ctx context.Context) (*permission.RequestContext, error) {
	if rc := CallerFrom(ctx); rc != nil {
		return rc, nil
	}
	if auth, ok := ctx.Value(authenticatorKey{}).(Authenticator); ok && auth != nil {
		return auth(ctx)
	}
	return nil, nil
}

// sendServiceError returns se as the error (the name is the gRPC era's; kept so the services read
// as they did).
func sendServiceError(_ context.Context, se *crud.ServiceError) error { return se }

// rpcError passes a ServiceError through; anything else is an internal error the HTTP layer logs.
func rpcError(_ context.Context, err error) error { return err }

func svcErrf(ctx context.Context, httpStatus uint16, code, format string, args ...any) error {
	return svcErr(ctx, httpStatus, code, fmt.Sprintf(format, args...))
}

// jsonOr is raw when it holds valid JSON, else fallback ("null", "{}").
func jsonOr(raw []byte, fallback string) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
}
