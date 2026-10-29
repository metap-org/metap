package runtime

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit builds a per-peer-IP token-bucket rate limiter — ported from metap-runtime's
// rate_limit::build (tower_governor). burstSize tokens replenish one every perMillisecond.
// Keyed by peer IP (r.RemoteAddr), same PeerIpKeyExtractor choice the Rust version makes and the
// same caveat it documents: behind a load balancer that doesn't strip/rewrite the client's real
// IP, every caller collapses into one shared bucket.
//
// Known simplification vs. the Rust tower_governor version: that library evicts idle per-key
// buckets automatically; this port holds one *rate.Limiter per IP seen for the life of the
// process (an unbounded-key-set memory growth under a very large number of distinct peer IPs) —
// acceptable for Wave 1, flagged for revisit once this is wired into a real long-running binary.
func RateLimit(perMillisecond time.Duration, burstSize int) func(http.Handler) http.Handler {
	var mu sync.Mutex
	limiters := map[string]*rate.Limiter{}

	limiterFor := func(key string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		l, ok := limiters[key]
		if !ok {
			l = rate.NewLimiter(rate.Every(perMillisecond), burstSize)
			limiters[key] = l
		}
		return l
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			if !limiterFor(host).Allow() {
				ServiceErrorResponse(w, http.StatusTooManyRequests, "too_many_requests",
					"Too many requests. Retry later.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
