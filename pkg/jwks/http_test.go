package jwks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerServesTheCurrentJwkSetAsJSON(t *testing.T) {
	key, err := GenerateJwksKeyPair("kid-1")
	require.NoError(t, err)
	store := NewSharedKeyStore(NewJwksKeyStore(key))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()
	Handler(store).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "got %d", w.Code)
	var body JwkSet
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Keys, 1, "got %d keys", len(body.Keys))
	require.Equal(t, "kid-1", body.Keys[0].Kid, "got %q", body.Keys[0].Kid)
	require.Equal(t, "OKP", body.Keys[0].Kty, "got %q", body.Keys[0].Kty)
}

func TestHandlerReflectsARotationAppliedAfterwards(t *testing.T) {
	keyA, err := GenerateJwksKeyPair("kid-a")
	require.NoError(t, err)
	store := NewSharedKeyStore(NewJwksKeyStore(keyA))

	keyB, err := GenerateJwksKeyPair("kid-b")
	require.NoError(t, err)
	require.NoError(t, store.Mutate(func(s *JwksKeyStore) error {
		s.AddKey(keyB)
		return nil
	}))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()
	Handler(store).ServeHTTP(w, req)

	var body JwkSet
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Keys, 2, "got %d keys", len(body.Keys))
}
