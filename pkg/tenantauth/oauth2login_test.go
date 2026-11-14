package tenantauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func oauth2TestConfig(idpURL string) *OAuth2LoginConfig {
	return &OAuth2LoginConfig{
		AuthorizeURL:      idpURL + "/authorize",
		TokenURL:          idpURL + "/token",
		UserinfoURL:       idpURL + "/user",
		ClientID:          "test-client",
		ClientSecretRef:   "TEST_OAUTH2_CLIENT_SECRET",
		RedirectURI:       "http://localhost:3000/auth/oauth2/00000000-0000-0000-0000-000000000000/callback",
		Scopes:            []string{"read:user"},
		PostLoginRedirect: "http://localhost:5173/auth/oauth2/callback",
		SubjectField:      "id",
		EmailField:        "email",
	}
}

func oauth2MockIdP(t *testing.T, subject any, email string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    subject,
			"email": email,
			"name":  "Ignored Field",
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// The full client-side protocol this package drives, against a mocked IdP — code exchange, then
// an authenticated userinfo call, exactly as GitHub's own OAuth app flow works (no id_token,
// unlike OIDC — this is the whole reason this provider exists as a separate code path).
func TestAuthorizeAndVerifyCallbackRecoversTheIdpIdentityViaUserinfo(t *testing.T) {
	idp := oauth2MockIdP(t, 987654321, "person@example.com")
	config := oauth2TestConfig(idp.URL)

	authURL, _, pkceVerifier, err := OAuth2LoginAuthorizeURL(config, "shhh")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(authURL, config.AuthorizeURL), "expected %q to start with %q", authURL, config.AuthorizeURL)

	identity, err := OAuth2LoginVerifyCallback(context.Background(), config, "shhh", "any-code", pkceVerifier)
	require.NoError(t, err)
	require.Equal(t, "person@example.com", identity.Email, "got email %q", identity.Email)
	require.Equal(t, "987654321", identity.ExternalSubject, "got external subject %q", identity.ExternalSubject)
}

// A provider whose id field is a string (matching OIDC's own sub convention) must work
// identically — the normalization must not assume a number.
func TestStringShapedSubjectFieldAlsoWorks(t *testing.T) {
	idp := oauth2MockIdP(t, "sub-string-42", "another@example.com")
	config := oauth2TestConfig(idp.URL)

	_, _, pkceVerifier, err := OAuth2LoginAuthorizeURL(config, "shhh")
	require.NoError(t, err)
	identity, err := OAuth2LoginVerifyCallback(context.Background(), config, "shhh", "any-code", pkceVerifier)
	require.NoError(t, err)
	require.Equal(t, "sub-string-42", identity.ExternalSubject, "got %q", identity.ExternalSubject)
}

// A configured subjectField/emailField that doesn't match a provider's real response shape must
// fail loudly, not silently fall back to some default.
func TestMissingConfiguredFieldInTheUserinfoResponseIsAnError(t *testing.T) {
	idp := oauth2MockIdP(t, "some-subject", "person@example.com")
	config := oauth2TestConfig(idp.URL)
	config.SubjectField = "not_a_real_field"

	_, _, pkceVerifier, err := OAuth2LoginAuthorizeURL(config, "shhh")
	require.NoError(t, err)
	_, err = OAuth2LoginVerifyCallback(context.Background(), config, "shhh", "any-code", pkceVerifier)
	require.Error(t, err, "expected an error")
}

// Default field names ("id"/"email") apply when a tenant's config doesn't set them.
func TestConfigDefaultsSubjectAndEmailFieldNamesWhenOmitted(t *testing.T) {
	minimal := []byte(`{
		"authorizeUrl": "https://idp.example/authorize",
		"tokenUrl": "https://idp.example/token",
		"userinfoUrl": "https://idp.example/user",
		"clientId": "c",
		"clientSecretRef": "REF",
		"redirectUri": "https://app.example/callback",
		"postLoginRedirect": "https://app.example/"
	}`)
	var config OAuth2LoginConfig
	require.NoError(t, json.Unmarshal(minimal, &config))
	require.Equal(t, "id", config.SubjectField, "got %+v", config)
	require.Equal(t, "email", config.EmailField, "got %+v", config)
	require.Len(t, config.Scopes, 0, "got %+v", config)
}

func TestRequireHTTPSOrLoopback(t *testing.T) {
	cases := []struct {
		raw     string
		allowed bool
	}{
		{"https://idp.example/user", true},
		{"http://idp.example/user", false},
		{"http://127.0.0.1:8080/user", true},
		{"http://localhost:8080/user", true},
		{"http://[::1]:8080/user", true},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		require.NoError(t, err)
		gotErr := requireHTTPSOrLoopback(u)
		require.False(t, c.allowed && gotErr != nil, "%q should be allowed, got %v", c.raw, gotErr)
		require.False(t, !c.allowed && gotErr == nil, "%q should be rejected", c.raw)
	}
}
