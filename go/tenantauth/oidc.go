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
}

type VerifiedIdentity struct {
	Email           string
	ExternalSubject string
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

func oidcProviderAndVerifier(ctx context.Context, config *OidcConfig, clientSecret string) (*oidc.Provider, *oauth2.Config, error) {
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
// this function never reads an env var itself, matching the Rust original's own signature.
func OidcAuthorizeURL(ctx context.Context, config *OidcConfig, clientSecret string) (authorizeURL, csrfToken, nonce, pkceVerifier string, err error) {
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
func OidcVerifyCallback(ctx context.Context, config *OidcConfig, clientSecret, code, nonce, pkceVerifier string) (*VerifiedIdentity, error) {
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

	var claims struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, err
	}
	if claims.Email == "" {
		return nil, fmt.Errorf("IdP did not return an email claim")
	}

	return &VerifiedIdentity{Email: claims.Email, ExternalSubject: idToken.Subject}, nil
}
