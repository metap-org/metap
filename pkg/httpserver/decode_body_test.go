package httpserver

import (
	"github.com/metap-org/metap/pkg/kit/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeJSONBodyAnswersForTheHandler(t *testing.T) {
	type body struct{ A string }
	cases := []struct {
		name, payload string
		ok            bool
		status        int
		code          string
	}{
		{"valid", `{"A":"x"}`, true, 200, ""},
		{"malformed", `{`, false, 400, "validation_failed"},
		{"oversized", `{"A":"` + strings.Repeat("x", 2<<20) + `"}`, false, 413, "payload_too_large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var b body
			ok := DecodeJSONBody(rec, httptest.NewRequest("POST", "/", strings.NewReader(c.payload)), &b)
			require.Equal(t, c.ok, ok)
			if c.ok {
				assert.Equal(t, "x", b.A)
				return
			}
			assert.Equal(t, c.status, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code":"`+c.code+`"`)
		})
	}
}

// Recover has to sit inside RequestContext, or a panicked request gets no x-request-id and an
// error body without requestId/traceId. This mirrors BuildRouter's order for those layers.
func TestRecoveredPanicStillCarriesRequestIDs(t *testing.T) {
	h := httpx.GenerateRequestIDs(httpx.RequestContext(httpx.Recover(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("x-request-id"))
	assert.Contains(t, rec.Body.String(), `"code":"internal_error"`)
	assert.Contains(t, rec.Body.String(), `"requestId"`)
	assert.Contains(t, rec.Body.String(), `"traceId"`)
}
