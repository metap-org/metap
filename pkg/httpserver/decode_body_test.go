package httpserver

import (
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
