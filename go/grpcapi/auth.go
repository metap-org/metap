// Turns an incoming RPC's metadata into a permission.RequestContext — the gRPC counterpart to
// httpserver's authenticate. Ported from crates/metap-grpc/src/auth.rs.
//
// On-behalf-of-user only, for now — every RPC authenticates via a forwarded bearer token
// ("authorization" gRPC metadata), either JWKS-verified or the platform's own static per-app
// key, whichever the caller configured. Pure machine-to-machine calls with no originating user
// (mTLS-authenticated) are a deliberately deferred extension point, same as the Rust original —
// a request with no "authorization" metadata is rejected rather than silently treated as some
// guessed-at service identity.
package grpcapi

import (
	"context"
	"sync/atomic"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/jwks"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/runtime"
)

// AuthConfig bundles everything authenticate needs beyond the request itself — mirrors the Rust
// AuthConfig struct (one struct rather than loose fields, the same shape AppState's own fields
// already follow).
type AuthConfig struct {
	Verifier               *jwks.TokenVerifier
	Router                 *control.Router
	AuthContextEntity      string
	Metadata               *atomic.Pointer[metadata.MetadataRegistry]
	ContextAttributesCache *control.ContextAttributesCache
}

func bearerTokenFromMetadata(md grpcmd.MD) (string, error) {
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	token, ok := runtime.ParseBearer(values[0])
	if !ok {
		return "", status.Error(codes.Unauthenticated, "authorization metadata must be a Bearer token")
	}
	return token, nil
}

// authenticate reads "authorization" off the incoming context's gRPC metadata, verifies it
// against config.Verifier, and resolves the full RequestContext (roles, context attributes, any
// OAuth2 grant) exactly as httpserver's own authenticate does for REST.
func authenticate(ctx context.Context, config AuthConfig) (*permission.RequestContext, error) {
	md, ok := grpcmd.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	token, err := bearerTokenFromMetadata(md)
	if err != nil {
		return nil, err
	}

	claims, err := jwks.DecodeWithVerifier(ctx, token, config.Verifier, nil)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
	}

	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "token is missing required claims")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "token is missing required claims")
	}
	functionID := ""
	if claims.FunctionID != nil {
		functionID = *claims.FunctionID
	}

	rc, err := control.ResolveRequestContext(ctx, config.Router, tenantID, userID, functionID,
		config.AuthContextEntity, config.Metadata, config.ContextAttributesCache)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "failed to resolve roles")
	}

	// Same as metap-http's AuthContext: a token minted for an OAuth2 client carries a granted
	// scope, and every transport must surface it or a scoped token would be served as a full
	// session over gRPC (including via graphql-gateway, which forwards the caller's own token).
	if claims.Scope != nil {
		clientID := ""
		hasClientID := claims.ClientID != nil
		if hasClientID {
			clientID = *claims.ClientID
		}
		rc.ApplyOAuthGrant(*claims.Scope, clientID, hasClientID)
	}
	return rc, nil
}
