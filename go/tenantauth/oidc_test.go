package tenantauth

// Ported from the Rust metap-auth crate's tests/oidc_e2e.rs (the wire-protocol tests; the
// DB-touching JIT-provisioning test is ported in tenantauth_postgres_test.go instead). A
// hand-signed RS256 id_token (via golang-jwt/jwt + a freshly generated RSA keypair) stands in for
// what a real IdP would return, same approach the Rust original takes with jsonwebtoken+rsa.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type testIdp struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	nextID *string // next id_token to return from /token, set by respondWithIDToken
}

// start spins up a mock IdP serving discovery + JWKS, both fixed for the server's lifetime — the
// per-test mock only needs to vary the /token response (the id_token's claims).
func startTestIdp(t *testing.T) *testIdp {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIdp{key: key, kid: "test-key"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := idp.server.URL
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(bigEndianBytes(key.PublicKey.E))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": idp.kid, "n": n, "e": e},
			},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if idp.nextID == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"id_token":     *idp.nextID,
			"expires_in":   300,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func bigEndianBytes(v int) []byte {
	b := []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

func (idp *testIdp) issuerURL() string { return idp.server.URL }

// respondWithIDToken signs and registers the id_token the mocked /token endpoint will return on
// the next exchange — nonce must be the exact value OidcAuthorizeURL generated, exactly as a
// real callback's id_token must echo back the nonce the client sent at /authorize.
func (idp *testIdp) respondWithIDToken(clientID, subject, email, nonce string) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":   idp.issuerURL(),
		"sub":   subject,
		"aud":   clientID,
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": nonce,
		"email": email,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = idp.kid
	idToken, err := token.SignedString(idp.key)
	if err != nil {
		panic(err)
	}
	idp.nextID = &idToken
}

func oidcTestConfig(idp *testIdp) *OidcConfig {
	return &OidcConfig{
		IssuerURL:         idp.issuerURL(),
		ClientID:          "test-client",
		ClientSecretRef:   "TEST_OIDC_CLIENT_SECRET",
		RedirectURI:       "http://localhost:3000/auth/oidc/00000000-0000-0000-0000-000000000000/callback",
		Scopes:            []string{"openid", "email"},
		PostLoginRedirect: "http://localhost:5173/auth/oidc/callback",
	}
}

// The full client-side protocol this package drives, against a real (mocked) IdP — not just
// isolated function calls: OidcAuthorizeURL's generated nonce must be exactly what
// OidcVerifyCallback receives back for the whole thing to succeed, same as a real redirect round
// trip.
func TestAuthorizeAndVerifyCallbackRoundTripRecoversTheIdpIdentity(t *testing.T) {
	idp := startTestIdp(t)
	config := oidcTestConfig(idp)

	authURL, _, nonce, pkceVerifier, err := OidcAuthorizeURL(context.Background(), config, "shhh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authURL, idp.issuerURL()+"/authorize") {
		t.Fatalf("got %q", authURL)
	}

	idp.respondWithIDToken(config.ClientID, "idp-subject-42", "person@example.com", nonce)

	identity, err := OidcVerifyCallback(context.Background(), config, "shhh", "any-code", nonce, pkceVerifier)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "person@example.com" {
		t.Fatalf("got email %q", identity.Email)
	}
	if identity.ExternalSubject != "idp-subject-42" {
		t.Fatalf("got subject %q", identity.ExternalSubject)
	}
}

// A tampered/mismatched nonce (someone replaying an old id_token, or a CSRF-state mixup) must
// be rejected.
func TestWrongNonceIsRejected(t *testing.T) {
	idp := startTestIdp(t)
	config := oidcTestConfig(idp)

	_, _, _, pkceVerifier, err := OidcAuthorizeURL(context.Background(), config, "shhh")
	if err != nil {
		t.Fatal(err)
	}
	idp.respondWithIDToken(config.ClientID, "idp-subject-42", "person@example.com", "correct-nonce")

	if _, err := OidcVerifyCallback(context.Background(), config, "shhh", "any-code", "wrong-nonce", pkceVerifier); err == nil {
		t.Fatal("expected a mismatched nonce to fail verification")
	}
}

func TestResolveClientSecretEnv(t *testing.T) {
	t.Setenv("TEST_RESOLVE_CLIENT_SECRET", "the-secret")
	got, err := ResolveClientSecretEnv("TEST_RESOLVE_CLIENT_SECRET")
	if err != nil || got != "the-secret" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := ResolveClientSecretEnv("TEST_RESOLVE_CLIENT_SECRET_DOES_NOT_EXIST"); err == nil {
		t.Fatal("expected an error for an unset env var")
	}
}
