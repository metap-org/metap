// Plain OAuth2 authorization-code login provider — a tenant logging its users in against an IdP
// that speaks OAuth2 but not OIDC (no discovery document, no id_token; GitHub's own OAuth apps
// are the canonical example). Distinct from oidc.go in exactly one way that matters: there is no
// signed identity token to decode, so identity is resolved by calling a configured userinfo
// endpoint with the freshly obtained access token and reading two fields out of whatever JSON it
// returns.
//
// Reuses the same VerifiedIdentity{Email, ExternalSubject} shape oidc.go produces, so a callback
// handler (not yet ported) and FindExternalUser/JitProvisionExternalUser serve both providers
// without a second code path.
package tenantauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type OAuth2LoginConfig struct {
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	// UserinfoURL is called with Authorization: Bearer <access_token> right after the code
	// exchange — the substitute for an OIDC id_token's claims.
	UserinfoURL string `json:"userinfoUrl"`
	ClientID    string `json:"clientId"`
	// ClientSecretRef: same "name of an env var, never the secret itself" contract as
	// OidcConfig.ClientSecretRef.
	ClientSecretRef   string   `json:"clientSecretRef"`
	RedirectURI       string   `json:"redirectUri"`
	Scopes            []string `json:"scopes,omitempty"`
	PostLoginRedirect string   `json:"postLoginRedirect"`
	// SubjectField: field name in the userinfo JSON response holding the provider's stable user
	// identifier — configurable because it varies by provider (GitHub: "id", a numeric field;
	// many others: "sub", matching OIDC's own claim name). Defaults to "id".
	SubjectField string `json:"subjectField"`
	// EmailField: field name holding the user's email. Defaults to "email".
	EmailField string `json:"emailField"`
}

// UnmarshalJSON applies the subjectField="id"/emailField="email" defaults the Rust
// #[serde(default = "...")] attributes give OAuth2LoginConfig, so a tenant config that omits
// them still parses the same way.
func (c *OAuth2LoginConfig) UnmarshalJSON(data []byte) error {
	type raw OAuth2LoginConfig
	aux := raw{SubjectField: "id", EmailField: "email"}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*c = OAuth2LoginConfig(aux)
	return nil
}

func OAuth2LoginConfigFor(ctx context.Context, db DBTX, tenantID uuid.UUID) (*OAuth2LoginConfig, error) {
	var raw []byte
	err := db.QueryRow(ctx,
		"SELECT config FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oauth2' AND enabled = true",
		tenantID).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var cfg OAuth2LoginConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func oauth2LoginClient(config *OAuth2LoginConfig, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: config.AuthorizeURL, TokenURL: config.TokenURL},
		RedirectURL:  config.RedirectURI,
		Scopes:       config.Scopes,
	}
}

// requireHTTPSOrLoopback: https only, with a loopback exception for a local mock IdP (this
// package's own tests bind a plain http://127.0.0.1:<port> listener — same allowance RFC 8252
// makes for a native app's own http://localhost redirect URI) — a real tenant-admin-configured
// userinfoURL pointing anywhere else over plain http would send the access token in cleartext.
func requireHTTPSOrLoopback(u *url.URL) error {
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	isLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if isLoopback {
		return nil
	}
	return fmt.Errorf("userinfo_url must use https (got %q)", u.Scheme)
}

// OAuth2LoginAuthorizeURL returns (authorizeURL, csrfToken, pkceVerifier) — one fewer element
// than OidcAuthorizeURL's tuple (no nonce: that's an OIDC-specific replay defense tied to the
// id_token this provider never receives).
func OAuth2LoginAuthorizeURL(config *OAuth2LoginConfig, clientSecret string) (authorizeURL, csrfToken, pkceVerifier string, err error) {
	client := oauth2LoginClient(config, clientSecret)
	csrfToken = generateRandomToken()
	pkceVerifier = oauth2.GenerateVerifier()
	authorizeURL = client.AuthCodeURL(csrfToken, oauth2.S256ChallengeOption(pkceVerifier))
	return authorizeURL, csrfToken, pkceVerifier, nil
}

// OAuth2LoginVerifyCallback exchanges the callback's code, then calls userinfoURL with the
// resulting access token and reads subjectField/emailField out of the JSON response.
func OAuth2LoginVerifyCallback(ctx context.Context, config *OAuth2LoginConfig, clientSecret, code, pkceVerifier string) (*VerifiedIdentity, error) {
	client := oauth2LoginClient(config, clientSecret)
	httpClient := noRedirectHTTPClient()
	ctxWithClient := context.WithValue(ctx, oauth2.HTTPClient, httpClient)

	token, err := client.Exchange(ctxWithClient, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return nil, fmt.Errorf("OAuth2 code exchange failed: %w", err)
	}

	// The access token goes out in this request's Authorization header — refuse to send it
	// anywhere but https, since userinfoURL is tenant-admin-configured and nothing upstream of
	// this call validates its scheme.
	userinfoURL, err := url.Parse(config.UserinfoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid userinfo_url: %w", err)
	}
	if err := requireHTTPSOrLoopback(userinfoURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo request returned status %d", resp.StatusCode)
	}
	var userinfo map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&userinfo); err != nil {
		return nil, err
	}

	subjectRaw, ok := userinfo[config.SubjectField]
	if !ok {
		return nil, fmt.Errorf("userinfo response has no %q field", config.SubjectField)
	}
	// A subject id is legitimately either a JSON string or number depending on provider
	// (GitHub's id is numeric) — normalized to a string either way, since ExternalSubject is
	// stored/compared as text regardless of the source shape.
	externalSubject := jsonScalarToString(subjectRaw)

	emailRaw, ok := userinfo[config.EmailField]
	email, isStr := emailRaw.(string)
	if !ok || !isStr {
		return nil, fmt.Errorf("userinfo response has no %q string field", config.EmailField)
	}

	return &VerifiedIdentity{Email: email, ExternalSubject: externalSubject}, nil
}

func jsonScalarToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// encoding/json decodes a JSON number into Go's float64 by default; GitHub's own
		// numeric id field has no fractional part, so an integer format matches what the Rust
		// original's serde_json::Value::to_string() produces for a JSON integer.
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'g', -1, 64)
	default:
		b, _ := json.Marshal(val)
		return strings.Trim(string(b), `"`)
	}
}
