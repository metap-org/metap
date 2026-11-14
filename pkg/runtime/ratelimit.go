package runtime

import (
	"github.com/metap-org/metap/pkg/kit/httpx"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit builds a per-peer-IP token-bucket rate limiter. burstSize tokens replenish one every
// perMillisecond.
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
