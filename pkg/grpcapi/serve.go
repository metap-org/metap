// Runs RecordService on its own listener — a second port alongside a binary's main HTTP listener.
package grpcapi

import (
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
)

// Per-peer token bucket for the gRPC listener: one token per rateInterval, up to rateBurst at once.
// The same numbers graphql-gateway uses for its HTTP listener.
const (
	rateInterval = 200 * time.Millisecond
	rateBurst    = 300
)

// NewServer builds the *grpc.Server (access logging + rate limiting, RecordService registered)
// without starting to serve — exposed separately from Serve so a caller that wants a stoppable
// handle (GracefulStop) can call srv.Serve(lis) itself.
//
// Same token-bucket shape/defaults as graphql-gateway's own rate limiter (200ms per token, burst
// 300) — see ratelimit.go's doc comment for why this transport needs its own small interceptor
// instead of reusing httpserver's RateLimit directly (that one wraps net/http, not grpc).
func NewServer(service *RecordService, actions ...*ActionService) *grpc.Server {
	limiter := newRateLimiter(rateInterval, rateBurst)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(callLogInterceptor, limiter.UnaryInterceptor()))
	pb.RegisterRecordServiceServer(srv, service)
	for _, a := range actions {
		pb.RegisterActionServiceServer(srv, a)
	}
	return srv
}

// Serve builds the server via NewServer and blocks serving addr until the listener/server stops.
// A caller that wants a background task spawns `go Serve(...)` itself — see OptionalServe below.
func Serve(addr string, service *RecordService) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	return NewServer(service).Serve(lis)
}

// OptionalServeConfig bundles what OptionalServe needs from a binary's own composition root —
// grouped the same way AuthConfig bundles its own inputs.
type OptionalServeConfig struct {
	// Backend is normally a *crud.CrudService, or a binary's own decorator around one.
	Backend                crud.RecordBackend
	Router                 *control.Router
	JWTPublicKey           *rsa.PublicKey
	AuthContextEntity      string
	Metadata               *atomic.Pointer[metadata.MetadataRegistry]
	ContextAttributesCache *control.ContextAttributesCache
	// TokenVerifierOverride: set this to a binary's own JWKS trust-root verifier once it has
	// opted into metap-jwks for its other transports, so gRPC can't silently keep verifying
	// against the old static key after everything else moved off it. nil preserves the
	// all-Static default below.
	TokenVerifierOverride *jwks.TokenVerifier
	// Actions, when set, also serves ActionService over the same listener, running the handlers
	// registered in it for the actions entities declare in EntityDefinition.Actions.
	Actions crud.ActionInvoker
}

// OptionalServe reads GRPC_ENABLED/GRPC_PORT (falling back to defaultPort) to decide whether to
// run gRPC at all, builds the matching AuthConfig, and spawns Serve in its own goroutine.
// Returns (nil, nil) when GRPC_ENABLED isn't set — a caller that wants gRPC unconditionally on
// calls Serve/NewServer directly instead. The returned *grpc.Server lets the caller
// GracefulStop() it on shutdown.
func OptionalServe(host string, defaultPort int, cfg OptionalServeConfig) (*grpc.Server, error) {
	if !env.Bool("GRPC_ENABLED") {
		return nil, nil
	}
	port := env.Int("GRPC_PORT", defaultPort)
	addr := fmt.Sprintf("%s:%d", host, port)

	verifier := cfg.TokenVerifierOverride
	if verifier == nil {
		verifier = &jwks.TokenVerifier{Kind: jwks.VerifierStatic, PublicKey: cfg.JWTPublicKey, Leeway: jwks.DefaultLeeway}
	}
	auth := AuthConfig{
		Verifier:               verifier,
		Router:                 cfg.Router,
		AuthContextEntity:      cfg.AuthContextEntity,
		Metadata:               cfg.Metadata,
		ContextAttributesCache: cfg.ContextAttributesCache,
	}
	service := NewRecordService(cfg.Backend, auth)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	var actions []*ActionService
	if cfg.Actions != nil {
		actions = append(actions, NewActionService(cfg.Actions, auth))
	}
	srv := NewServer(service, actions...)
	slog.Info("gRPC listening", "addr", addr)
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server exited with error", standard.LogError, err)
		}
	}()
	return srv, nil
}
