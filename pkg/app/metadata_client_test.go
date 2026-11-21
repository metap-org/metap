package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tenant admin names the URL the server fetches SAML metadata from, so the client must not be
// usable to reach the server's own network.
func TestMetadataClientRefusesInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)

	_, err := metadataHTTPClient().Get(srv.URL)
	require.Error(t, err, "loopback is refused")
	assert.Contains(t, err.Error(), "may not connect")

	t.Setenv("SSO_METADATA_ALLOW_PRIVATE_HOSTS", "true")
	res, err := metadataHTTPClient().Get(srv.URL)
	require.NoError(t, err, "a development stack may allow it")
	res.Body.Close()
}
