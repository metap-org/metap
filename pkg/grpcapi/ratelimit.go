// Per-peer-IP rate limiting for this package's own gRPC listener — ported from
// crates/metap-grpc/src/rate_limit.rs. Built directly on golang.org/x/time/rate (the same
// library httpserver's own RateLimit middleware uses), keyed by the real TCP peer address via
// peer.FromContext — unlike the HTTP layer's PeerIpKeyExtractor there is no X-Forwarded-For-style
// spoofing concern to weigh here, since this only ever sees the actual socket peer, never a
// caller-supplied header.
package grpcapi

import (
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// rateLimiter is a per-peer-IP token bucket — one *rate.Limiter per IP seen for the life of the
// process, same known simplification httpserver's own RateLimit documents (no idle-bucket
// eviction; acceptable for this wave, flagged for revisit under a very large number of distinct
// peer IPs).
type rateLimiter struct {
	perMillisecond time.Duration
	burstSize      int

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func newRateLimiter(perMillisecond time.Duration, burstSize int) *rateLimiter {
	return &rateLimiter{perMillisecond: perMillisecond, burstSize: burstSize, limiters: map[string]*rate.Limiter{}}
}

func (r *rateLimiter) limiterFor(key string) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.limiters[key]
	if !ok {
		l = rate.NewLimiter(rate.Every(r.perMillisecond), r.burstSize)
		r.limiters[key] = l
	}
	return l
}

// UnaryInterceptor returns a grpc.UnaryServerInterceptor enforcing this limiter. No peer IP on
// the request (a non-TCP transport) fails open rather than blocking every request — this
// package only ever serves over TCP today, so this branch is not currently reachable, just not
// assumed away (same posture as the Rust original).
func (r *rateLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			host, _, err := net.SplitHostPort(p.Addr.String())
			if err != nil {
				host = p.Addr.String()
			}
			if !r.limiterFor(host).Allow() {
				return nil, status.Error(codes.ResourceExhausted, "Too many requests.")
			}
		}
		return handler(ctx, req)
	}
}
