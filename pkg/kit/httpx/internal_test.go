package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRequireSecretAndInternalClient(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"n": r.URL.Query().Get("n")}})
	})
	srv := httptest.NewServer(RequireSecret("s3cret")(ok))
	defer srv.Close()

	var out struct{ N string }
	if err := NewInternalClient(srv.URL, "s3cret", time.Second).GetData(context.Background(), "/x", url.Values{"n": {"7"}}, &out); err != nil || out.N != "7" {
		t.Fatalf("with the secret: %v %+v", err, out)
	}
	for _, secret := range []string{"", "wrong"} {
		if err := NewInternalClient(srv.URL, secret, time.Second).GetData(context.Background(), "/x", nil, &out); err == nil {
			t.Fatalf("secret %q must be refused", secret)
		}
	}
	unset := httptest.NewServer(RequireSecret("")(ok))
	defer unset.Close()
	resp, err := http.Get(unset.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no secret configured must be 503, got %d", resp.StatusCode)
	}
}
