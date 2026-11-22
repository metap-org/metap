package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InternalSecretHeader carries the shared secret on service-to-service calls to a service's
// /internal routes, which are not for a person: no user token, one secret per callee.
const InternalSecretHeader = "X-Internal-Secret"

// RequireSecret guards a route that only other services call. With no secret configured it answers
// 503 rather than serve unauthenticated; a wrong or missing header is 401. The comparison is in
// constant time (SecretEqual).
func RequireSecret(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expected == "" {
				WriteError(w, http.StatusServiceUnavailable, "not_configured", "The internal secret of this service is not set.")
				return
			}
			if !SecretEqual(r.Header.Get(InternalSecretHeader), expected) {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "Invalid internal secret.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// InternalClient calls another service's /internal routes: a base URL, the shared secret, a client
// with a timeout. The reply is metap's {"data": ...} envelope.
type InternalClient struct {
	Base   string
	Secret string
	HTTP   *http.Client
}

// NewInternalClient returns a client for base with a request timeout.
func NewInternalClient(base, secret string, timeout time.Duration) *InternalClient {
	return &InternalClient{Base: strings.TrimRight(base, "/"), Secret: secret, HTTP: NewClient(timeout)}
}

// GetData GETs path (with query) and decodes the envelope's "data" into out. Any status but 200 is
// an error naming it.
func (c *InternalClient) GetData(ctx context.Context, path string, query url.Values, out any) error {
	target := c.Base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set(InternalSecretHeader, c.Secret)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", path, resp.StatusCode)
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	return json.Unmarshal(body.Data, out)
}
