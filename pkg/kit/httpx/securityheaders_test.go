package httpx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecurityHeadersSetsDefaultsWhenHandlerSetsNone(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Content-Security-Policy") != defaultCSP {
		require.FailNow(t, fmt.Sprintf("got CSP %q", w.Header().Get("Content-Security-Policy")))
	}
	require.Equal(t, "SAMEORIGIN", w.Header().Get("X-Frame-Options"), "expected X-Frame-Options to be set")
}

func TestSecurityHeadersRespectsHandlerOwnCSP(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		require.FailNow(t, fmt.Sprintf("got CSP %q, handler's own value must win", w.Header().Get("Content-Security-Policy")))
	}
}
