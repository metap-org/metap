// Package admin wires metap's user, tenant and access administration (roadmap 113-115) from its
// slices: users, account, roles, policies, owner, ssoconfig, credentials, settings, support, trail,
// tenants, apikeys, mfa, sessions and preferences, each a package of its own with types, rules, a store
// port and its Postgres adapter, and a service that only orchestrates (docs/guides/02-admin-slice-template.md
// in ../metap-docs). This package holds only what they share at the edge: the constructors that build a
// slice from an IdentityConfig (wiring.go), the caller (WithCaller) and the aliases httpapi and the
// products use. Requests and responses are plain structs, failures are *crud.ServiceError, and the caller
// is the RequestContext the HTTP layer authenticated.
package admin

import (
	"context"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/permission"
)

// WithCaller is ctx carrying the authenticated caller the services act as.
func WithCaller(ctx context.Context, rc *permission.RequestContext) context.Context {
	return kernel.WithCaller(ctx, rc)
}

// Authenticator resolves the caller when a service call needs it.
type Authenticator = kernel.Authenticator

// WithAuthenticator is ctx whose caller is resolved by auth at call time rather than up front: a token
// revoked between two calls made with the same ctx fails the second one.
func WithAuthenticator(ctx context.Context, auth Authenticator) context.Context {
	return kernel.WithAuthenticator(ctx, auth)
}

// CallerFrom is the caller WithCaller put in ctx; nil when none.
func CallerFrom(ctx context.Context) *permission.RequestContext { return kernel.CallerFrom(ctx) }
