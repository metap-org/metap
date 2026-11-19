package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteverifySendsSecretAndTokenAndReadsSuccess(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = map[string]string{"secret": r.Form.Get("secret"), "response": r.Form.Get("response"), "remoteip": r.Form.Get("remoteip")}
		if r.Form.Get("response") == "good" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	t.Cleanup(srv.Close)
	v := NewSiteverify(srv.URL, "s3cret")

	require.NoError(t, v.Verify(context.Background(), "good", "203.0.113.9"))
	assert.Equal(t, map[string]string{"secret": "s3cret", "response": "good", "remoteip": "203.0.113.9"}, got)
	assert.ErrorIs(t, v.Verify(context.Background(), "bad", ""), ErrFailed)
	assert.ErrorIs(t, v.Verify(context.Background(), "  ", ""), ErrFailed, "an empty token never reaches the provider")
}

func TestSiteverifyProviderFailureIsNotAPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(srv.Close)
	err := NewSiteverify(srv.URL, "s").Verify(context.Background(), "x", "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrFailed, "an outage is reported as such, not as a wrong answer")
}

func TestBuild(t *testing.T) {
	v, err := Build("", "")
	require.NoError(t, err)
	assert.Nil(t, v)
	_, err = Build("turnstile", "")
	assert.Error(t, err)
	_, err = Build("nope", "x")
	assert.Error(t, err)
	v, err = Build("Turnstile", "x")
	require.NoError(t, err)
	assert.NotNil(t, v)
}
