package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A definite 401 must fail immediately, not retry for the initial retry budget — a wrong
// service-account password is never going to fix itself by waiting.
func TestFailsFastOnARealUnauthorizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "invalid_credentials", "message": "nope"},
		})
	}))
	defer server.Close()

	started := time.Now()
	_, err := StartServiceTokenSource(context.Background(), server.Client(), server.URL+"/auth/login",
		"svc@internal.local", uuid.NewString())
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(started) >= serviceTokenInitialRetryBudget {
		t.Fatal("a definite 401 must not go through the retry budget")
	}
}

// The boot-race case this type exists for: the upstream isn't listening yet at the first
// attempt, then comes up a moment later — StartServiceTokenSource must ride that out.
func TestRetriesAConnectionRefusedUntilTheUpstreamComesUp(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"token": "fake-token"}})
	}))
	addr := server.Listener.Addr().String()
	_ = server.Listener.Close() // nothing listens here yet — the first attempt(s) must see connection-refused

	go func() {
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		server.Listener = l
		server.Start()
	}()

	source, err := StartServiceTokenSource(context.Background(), http.DefaultClient, "http://"+addr+"/auth/login",
		"svc@internal.local", uuid.NewString())
	if err != nil {
		t.Fatalf("should ride out the boot race and eventually log in: %v", err)
	}
	if source.Current() != "fake-token" {
		t.Fatalf("got token %q", source.Current())
	}
	server.Close()
}
