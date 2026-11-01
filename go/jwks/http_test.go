package jwks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerServesTheCurrentJwkSetAsJSON(t *testing.T) {
	key, err := GenerateJwksKeyPair("kid-1")
	if err != nil {
		t.Fatal(err)
	}
	store := NewSharedKeyStore(NewJwksKeyStore(key))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()
	Handler(store).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var body JwkSet
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Keys) != 1 {
		t.Fatalf("got %d keys", len(body.Keys))
	}
	if body.Keys[0].Kid != "kid-1" {
		t.Fatalf("got %q", body.Keys[0].Kid)
	}
	if body.Keys[0].Kty != "OKP" {
		t.Fatalf("got %q", body.Keys[0].Kty)
	}
}

func TestHandlerReflectsARotationAppliedAfterwards(t *testing.T) {
	keyA, err := GenerateJwksKeyPair("kid-a")
	if err != nil {
		t.Fatal(err)
	}
	store := NewSharedKeyStore(NewJwksKeyStore(keyA))

	keyB, err := GenerateJwksKeyPair("kid-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(func(s *JwksKeyStore) error {
		s.AddKey(keyB)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()
	Handler(store).ServeHTTP(w, req)

	var body JwkSet
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Keys) != 2 {
		t.Fatalf("got %d keys", len(body.Keys))
	}
}
