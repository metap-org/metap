// OIDC provider — the only provider here that's a genuinely separate implementation from
// LocalPasswordProvider (redirect + callback, not a single verify-and-done call). A tenant's
// config lives in tenant_auth_configs.config (jsonb), never a raw secret: ClientSecretRef names
// an env var.
//
// No discovery-result caching yet — every login/callback re-fetches the IdP's provider metadata
// fresh. Correct first, simpler to test; worth revisiting once there's a real tenant hitting this
// in practice.
package tenantauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type OidcConfig struct {
	IssuerURL string `json:"issuerUrl"`
	ClientID  string `json:"clientId"`
	// ClientSecretRef: name of an env var holding the real client secret — never the secret
	// itself (this struct round-trips through tenant_auth_configs.config, a plain jsonb column,
	// no encryption at that layer).
	ClientSecretRef string   `json:"clientSecretRef"`
	RedirectURI     string   `json:"redirectUri"`
	Scopes          []string `json:"scopes,omitempty"`
	// PostLoginRedirect: where the browser is sent after a successful callback — the frontend
	// origin, not this API's own origin.
	PostLoginRedirect string `json:"postLoginRedirect"`
	// EmailDomains (roadmap 115 M7): when set, only addresses in these domains may sign in or be
	// provisioned through this IdP, and the domain routes a person to it at sign-in
	// (GET /auth/sso/discover). Domain ownership is not verified, so discovery is a convenience and
	// the callback's check is the safeguard.
	EmailDomains []string `json:"emailDomains,omitempty"`
	// DefaultRole is the role a person provisioned on first SSO sign-in starts with ("" = none).
	DefaultRole string `json:"defaultRole,omitempty"`
	// Enforced turns password sign-in off for this tenant's members, the owner excepted (M8).
	Enforced bool `json:"enforced,omitempty"`
	// RoleClaim names the id_token claim that carries the person's groups or roles (a string or a
	// list of strings), and RoleMap says which tenant role each value stands for. At every sign-in
	// the mapped roles are brought in line with the claim; a role not named in RoleMap is never
	// touched, so what an admin grants by hand stays.
	RoleClaim string            `json:"roleClaim,omitempty"`
	RoleMap   map[string]string `json:"roleMap,omitempty"`
}

// ManagedRoles is every tenant role the identity provider's claim decides (RoleMap's values).
func (c *OidcConfig) ManagedRoles() []string {
	seen := map[string]bool{}
	var out []string
	for _, role := range c.RoleMap {
		if !seen[role] {
			seen[role] = true
			out = append(out, role)
		}
	}
	sort.Strings(out)
	return out
}

// MappedRoles is the managed roles the given claim values grant.
func (c *OidcConfig) MappedRoles(claimValues []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range claimValues {
		if role, ok := c.RoleMap[v]; ok {
			out[role] = true
		}
	}
	return out
}

type VerifiedIdentity struct {
	Email string
	// EmailVerified is true only when the identity provider said so (`email_verified` true). An
	// address it did not vouch for must not be used to find, link or create a member: anyone can
	// register at some providers with someone else's address.
	EmailVerified   bool
	ExternalSubject string
	// Roles are the values of the configured role claim (empty without one).
	Roles []string
}

// OidcConfigFor: a tenant's enabled OIDC config, or (nil, nil) if it has none.
func OidcConfigFor(ctx context.Context, db DBTX, tenantID uuid.UUID) (*OidcConfig, error) {
	var raw []byte
	err := db.QueryRow(ctx,
		"SELECT config FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oidc' AND enabled = true",
		tenantID).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var cfg OidcConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ResolveClientSecretEnv(clientSecretRef string) (string, error) {
	v, ok := os.LookupEnv(clientSecretRef)
	if !ok {
		return "", fmt.Errorf("env var %s (OIDC client secret ref) is not set", clientSecretRef)
	}
	return v, nil
}

// noRedirectHTTPClient: no redirect-follow — an IdP redirecting our own discovery/token requests
// somewhere else is exactly the SSRF shape to avoid, not a legitimate flow.
func noRedirectHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func oidcProviderAndVerifier(
	ctx context.Context,
	config *OidcConfig,
	clientSecret string,
) (*oidc.Provider, *oauth2.Config, error) {
	ctx = oidc.ClientContext(ctx, noRedirectHTTPClient())
	provider, err := oidc.NewProvider(ctx, config.IssuerURL)
	if err != nil {
		return nil, nil, err
	}
	oauthConfig := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  config.RedirectURI,
		Scopes:       config.Scopes,
	}
	return provider, oauthConfig, nil
}

// OidcAuthorizeURL returns (authorizeURL, csrfToken, nonce, pkceVerifier) — the caller is
// responsible for stashing csrfToken -> (tenantID, nonce, pkceVerifier) somewhere that survives
// until the callback, since this package has no HTTP-session concept of its own. clientSecret is
// resolved by the caller (ResolveClientSecretEnv, or any other means) and passed in explicitly —
// this function never reads an env var itself.
func OidcAuthorizeURL(
	ctx context.Context,
	config *OidcConfig,
	clientSecret string,
) (authorizeURL, csrfToken, nonce, pkceVerifier string, err error) {
	_, oauthConfig, err := oidcProviderAndVerifier(ctx, config, clientSecret)
	if err != nil {
		return "", "", "", "", err
	}

	csrfToken = generateRandomToken()
	nonce = generateRandomToken()
	pkceVerifier = oauth2.GenerateVerifier()

	authorizeURL = oauthConfig.AuthCodeURL(csrfToken, oidc.Nonce(nonce), oauth2.S256ChallengeOption(pkceVerifier))
	return authorizeURL, csrfToken, nonce, pkceVerifier, nil
}

// OidcVerifyCallback exchanges the callback's code for tokens and verifies the id_token's
// signature + nonce (go-oidc does the actual JWKS fetch/signature check — no crypto implemented
// here). Checking the nonce is this function's own responsibility (go-oidc's IDToken.Nonce field
// is populated but deliberately not auto-verified by the library, per its own doc comment — "This
// package does NOT provide verification on the value of this field").
func OidcVerifyCallback(
	ctx context.Context,
	config *OidcConfig,
	clientSecret, code, nonce, pkceVerifier string,
) (*VerifiedIdentity, error) {
	httpClient := noRedirectHTTPClient()
	ctxWithClient := oidc.ClientContext(ctx, httpClient)
	provider, oauthConfig, err := oidcProviderAndVerifier(ctxWithClient, config, clientSecret)
	if err != nil {
		return nil, err
	}

	token, err := oauthConfig.Exchange(ctxWithClient, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return nil, err
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, fmt.Errorf("IdP token response has no id_token")
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: config.ClientID})
	idToken, err := verifier.Verify(ctxWithClient, rawIDToken)
	if err != nil {
		return nil, err
	}
	if idToken.Nonce != nonce {
		return nil, fmt.Errorf("id_token nonce does not match the expected value")
	}

	var claims map[string]json.RawMessage
	if err := idToken.Claims(&claims); err != nil {
		return nil, err
	}
	var email string
	_ = json.Unmarshal(claims["email"], &email)
	if email == "" {
		return nil, fmt.Errorf("IdP did not return an email claim")
	}

	identity := &VerifiedIdentity{Email: email, EmailVerified: claimTrue(claims["email_verified"]), ExternalSubject: idToken.Subject}
	if config.RoleClaim != "" {
		identity.Roles = claimStrings(claims[config.RoleClaim])
	}
	return identity, nil
}

// claimTrue reads a boolean claim; some providers send it as the string "true".
func claimTrue(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var str string
	return json.Unmarshal(raw, &str) == nil && strings.EqualFold(str, "true")
}

// claimStrings reads a claim that is one string or a list of strings.
func claimStrings(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}
