package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersSetsDefaultsWhenHandlerSetsNone(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Content-Security-Policy") != defaultCSP {
		t.Fatalf("got CSP %q", w.Header().Get("Content-Security-Policy"))
	}
	if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatal("expected X-Frame-Options to be set")
	}
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
		t.Fatalf("got CSP %q, handler's own value must win", w.Header().Get("Content-Security-Policy"))
	}
}
