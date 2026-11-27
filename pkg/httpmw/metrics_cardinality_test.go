package httpmw_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/metap-org/metap/pkg/httpmw"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
)

// /metrics is public, so the route label must never carry an attacker-chosen path.
func TestUnknownPathsShareOneSeries(t *testing.T) {
	m := httpmw.NewMetricsRegistry()
	r := router.New()
	r.Use(m.Middleware)
	r.Get("/health", func(http.ResponseWriter, *http.Request) {})

	for i := 0; i < 50; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("/scan-%d", i), nil))
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	assert.Equal(t, 1, strings.Count(body, `http_requests_total{`), "50 unknown paths must be one series")
	assert.Contains(t, body, `route="`+httpmw.UnmatchedRoute+`"`)
	assert.NotContains(t, body, "/scan-")
}
