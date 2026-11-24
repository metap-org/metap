// Package adminapi is metap's user, tenant and access administration (roadmap 113-115) as plain
// Go services: users, roles and permissions, invitations, the caller's own account (profile, MFA,
// sessions), API keys, tenants, audit search, support access, ownership, SSO and SCIM. Moved off
// gRPC (roadmap 116): requests and responses are plain structs (types.go), failures are
// *crud.ServiceError, and the caller is the RequestContext the HTTP layer authenticated
// (WithCaller). internal/httpapi serves them as REST.
package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

// WithCaller is ctx carrying the authenticated caller the services act as.
func WithCaller(ctx context.Context, rc *permission.RequestContext) context.Context {
	return kernel.WithCaller(ctx, rc)
}

// Authenticator resolves the caller when a service call needs it.
type Authenticator = kernel.Authenticator

// WithAuthenticator is ctx whose caller is resolved by auth at call time rather than up front:
// a token revoked between two calls made with the same ctx fails the second one.
func WithAuthenticator(ctx context.Context, auth Authenticator) context.Context {
	return kernel.WithAuthenticator(ctx, auth)
}

// CallerFrom is the caller WithCaller put in ctx; nil when none.
func CallerFrom(ctx context.Context) *permission.RequestContext { return kernel.CallerFrom(ctx) }

// resolveCaller is the caller in ctx (WithCaller, else WithAuthenticator).
func resolveCaller(ctx context.Context) (*permission.RequestContext, error) {
	return kernel.Resolve(ctx)
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
