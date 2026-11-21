package runtime

import (
	"github.com/metap-org/metap/pkg/kit/httpx"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit builds a per-peer-IP token-bucket rate limiter. burstSize tokens replenish one every
// perMillisecond.
func RateLimit(perMillisecond time.Duration, burstSize int) func(http.Handler) http.Handler {
	return RateLimitBehind(perMillisecond, burstSize, nil)
}

// RateLimitBehind is RateLimit for a service that sits behind proxies: requests are counted per
// httpx.ClientIP, so everyone arriving through the same gateway or load balancer does not share
// one bucket. trusted lists those proxies; with none it is exactly RateLimit.
func RateLimitBehind(perMillisecond time.Duration, burstSize int, trusted []netip.Prefix) func(http.Handler) http.Handler {
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
			host := httpx.ClientIP(r, trusted)
			// Reserve (not Allow) so a rejected request can carry a real Retry-After.
			reservation := limiterFor(host).Reserve()
			if delay := reservation.Delay(); delay > 0 {
				reservation.Cancel()
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
				httpx.ServiceErrorResponse(w, http.StatusTooManyRequests, "too_many_requests",
					"Too many requests. Retry later.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
