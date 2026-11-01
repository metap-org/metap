// Runs RecordService on its own listener — a second port alongside a binary's main HTTP
// listener, matching the Rust original's "second task in the same process" shape (see
// crates/metap-grpc/src/serve.rs's own doc comment for why not unifying onto one server). Ported
// from that file.
package grpcapi

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/grpcapi/pb"
	"github.com/metap-org/metap-go/jwks"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/runtime"
)

// logUnaryInterceptor is the per-RPC access log every REST route already gets via
// runtime.Trace — found needed live (2026-09-06, see the Rust serve.rs doc comment this ports)
// after a cross-service gRPC call produced zero log output on the target service's side. Logs
// the generic RPC method name (e.g. "/metap.crud.v1.RecordService/List") and latency/error —
// unlike the Rust version's per-entity span field, the handlers here don't thread a logger back
// out to add the entity name, which is a reasonable later refinement, not required for a working
// gRPC transport.
func logUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	latency := time.Since(start)
	if err != nil {
		slog.Error("grpc request failed", "method", info.FullMethod, "latency_ms", latency.Milliseconds(), "error", err)
	} else {
		slog.Info("grpc request completed", "method", info.FullMethod, "latency_ms", latency.Milliseconds())
	}
	return resp, err
}

// NewServer builds the *grpc.Server (rate limiting + access logging, RecordService registered)
// without starting to serve — exposed separately from Serve so a caller that wants a stoppable
// handle (GracefulStop) can call srv.Serve(lis) itself.
//
// Same token-bucket shape/defaults as graphql-gateway's own rate limiter (200ms per token, burst
// 300) — see ratelimit.go's doc comment for why this transport needs its own small interceptor
// instead of reusing httpserver's RateLimit directly (that one wraps net/http, not grpc).
func NewServer(service *RecordService) *grpc.Server {
	limiter := newRateLimiter(200*time.Millisecond, 300)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(limiter.UnaryInterceptor(), logUnaryInterceptor))
	pb.RegisterRecordServiceServer(srv, service)
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
}

// OptionalServe reads GRPC_ENABLED/GRPC_PORT (falling back to defaultPort) to decide whether to
// run gRPC at all, builds the matching AuthConfig, and spawns Serve in its own goroutine.
// Returns (nil, nil) when GRPC_ENABLED isn't set — a caller that wants gRPC unconditionally on
// calls Serve/NewServer directly instead. The returned *grpc.Server lets the caller
// GracefulStop() it on shutdown.
func OptionalServe(host string, defaultPort int, cfg OptionalServeConfig) (*grpc.Server, error) {
	if !runtime.FlagEnabled("GRPC_ENABLED") {
		return nil, nil
	}
	port := runtime.EnvOr("GRPC_PORT", defaultPort)
	addr := fmt.Sprintf("%s:%d", host, port)

	verifier := cfg.TokenVerifierOverride
	if verifier == nil {
		verifier = &jwks.TokenVerifier{Kind: jwks.VerifierStatic, PublicKey: cfg.JWTPublicKey, Leeway: 20 * time.Second}
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
	srv := NewServer(service)
	slog.Info("gRPC listening", "addr", addr)
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server exited with error", "error", err)
		}
	}()
	return srv, nil
}
