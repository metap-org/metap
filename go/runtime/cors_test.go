package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmptyOriginsUsesRestrictiveDefault(t *testing.T) {
	handler := CORS(CORSOptions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers with empty origins")
	}
}

func TestNonEmptyOriginsEchoesAllowedOriginWithCredentials(t *testing.T) {
	handler := CORS(CORSOptions{
		Origins: []string{"https://example.com"},
		Methods: []string{"GET", "POST"},
		Headers: []string{"Authorization", "Content-Type"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatalf("got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("expected credentials true")
	}
}

func TestDisallowedOriginGetsNoCorsHeaders(t *testing.T) {
	handler := CORS(CORSOptions{Origins: []string{"https://example.com"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers for a disallowed origin")
	}
}
