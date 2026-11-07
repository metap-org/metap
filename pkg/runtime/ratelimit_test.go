package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimitAllowsWithinBurst(t *testing.T) {
	handler := RateLimit(100*time.Millisecond, 2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:5555"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d: got status %d", i, w.Code)
	}
}

func TestRateLimitRejectsBeyondBurst(t *testing.T) {
	handler := RateLimit(time.Hour, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5555"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req)
	require.Equal(t, http.StatusTooManyRequests, w2.Code, "got status %d, want 429", w2.Code)
}

func TestRateLimitSetsRetryAfterHeaderWhenRejected(t *testing.T) {
	handler := RateLimit(time.Hour, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5555"
	handler.ServeHTTP(httptest.NewRecorder(), req)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req)
	require.Equal(t, http.StatusTooManyRequests, w2.Code, "got status %d, want 429", w2.Code)
	require.NotEqual(t, "", w2.Header().Get("Retry-After"), "expected a Retry-After header on a rejected request")
}

func TestRateLimitTracksDifferentPeersSeparately(t *testing.T) {
	handler := RateLimit(time.Hour, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "1.1.1.1:1"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "2.2.2.2:2"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w1.Code, "got (%d, %d), want both 200 — different peers must not share a bucket", w1.Code, w2.Code)
	require.Equal(t, http.StatusOK, w2.Code, "got (%d, %d), want both 200 — different peers must not share a bucket", w1.Code, w2.Code)
}
