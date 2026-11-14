package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerAppliesTimeoutsAndKeepsExplicitOnes(t *testing.T) {
	s := NewServer(":0", http.NotFoundHandler())
	assert.Equal(t, DefaultReadHeaderTimeout, s.ReadHeaderTimeout)
	assert.Equal(t, DefaultReadTimeout, s.ReadTimeout)
	assert.Equal(t, DefaultIdleTimeout, s.IdleTimeout)
	assert.Zero(t, s.WriteTimeout, "no default write timeout: a proxied response may be slow")

	own := &http.Server{ReadHeaderTimeout: 1}
	ApplyServerDefaults(own)
	assert.EqualValues(t, 1, own.ReadHeaderTimeout)
	assert.Equal(t, DefaultReadTimeout, own.ReadTimeout)
}

func TestDecodeJSONOrFail(t *testing.T) {
	type body struct{ A string }
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"ok", `{"A":"x"}`, 0, ""},
		{"malformed", `{`, 400, "invalid_request_body"},
		{"empty", ``, 400, "invalid_request_body"},
		{"too large", `{"A":"` + strings.Repeat("x", int(DefaultMaxBodyBytes)) + `"}`, 413, "payload_too_large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			var b body
			ok := DecodeJSONOrFail(rec, req, &b)
			if c.status == 0 {
				require.True(t, ok)
				assert.Equal(t, "x", b.A)
				return
			}
			require.False(t, ok)
			assert.Equal(t, c.status, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code":"`+c.code+`"`)
		})
	}
}

func TestRecoverReturnsTheStandardEnvelope(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, 500, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"internal_error","message":"Internal server error."}}`, rec.Body.String())
}

func TestRecoverLeavesAStartedResponseAlone(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestRecoverRepanicsAbortHandler(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	})
}

func TestMaxBodyCapsWhatTheHandlerReads(t *testing.T) {
	h := MaxBody(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 16)
		_, err := r.Body.Read(buf)
		if err != nil && err.Error() != "EOF" {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("0123456789")))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestSecretEqual(t *testing.T) {
	assert.True(t, SecretEqual("s3cret", "s3cret"))
	assert.False(t, SecretEqual("s3cre", "s3cret"))
	assert.False(t, SecretEqual("", "s3cret"))
	assert.False(t, SecretEqual("", ""), "an unset secret must never match")
	assert.False(t, SecretEqual("anything", ""))
}

func TestBearerToken(t *testing.T) {
	tok, ok := BearerToken("Bearer  abc ")
	assert.True(t, ok)
	assert.Equal(t, "abc", tok)
	for _, h := range []string{"", "Bearer ", "Basic abc", "bearer abc"} {
		_, ok := BearerToken(h)
		assert.False(t, ok, h)
	}
}
