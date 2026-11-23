// Package captcha verifies the proof-of-humanity token a browser widget hands back. Cloudflare
// Turnstile, hCaptcha and Google reCAPTCHA share one server-side contract — POST the secret and
// the token to a siteverify URL, read {"success": bool} — so one verifier covers all three.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

// Provider names accepted by Build.
const (
	ProviderTurnstile = "turnstile"
	ProviderHCaptcha  = "hcaptcha"
	ProviderReCaptcha = "recaptcha"
)

var siteverifyURLs = map[string]string{
	ProviderTurnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	ProviderHCaptcha:  "https://api.hcaptcha.com/siteverify",
	ProviderReCaptcha: "https://www.google.com/recaptcha/api/siteverify",
}

// ErrFailed: the token was missing, wrong, expired or already used.
var ErrFailed = errors.New("captcha verification failed")

// Verifier checks one token. remoteIP may be empty. A nil Verifier means CAPTCHA is off, which
// callers handle before calling.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// VerifierFunc adapts a function (tests, custom providers).
type VerifierFunc func(ctx context.Context, token, remoteIP string) error

func (f VerifierFunc) Verify(ctx context.Context, token, remoteIP string) error {
	return f(ctx, token, remoteIP)
}

type siteverify struct {
	url, secret string
	client      *http.Client
}

// Build returns the verifier for provider ("" = none, returns nil). A provider with no secret is
// a configuration error rather than a silently open door.
func Build(provider, secret string) (Verifier, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil, nil
	}
	u, ok := siteverifyURLs[provider]
	if !ok {
		return nil, fmt.Errorf("unknown CAPTCHA_PROVIDER %q (use turnstile, hcaptcha or recaptcha)", provider)
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("CAPTCHA_PROVIDER=%s needs CAPTCHA_SECRET", provider)
	}
	return NewSiteverify(u, secret), nil
}

// NewSiteverify talks to any siteverify-compatible endpoint.
func NewSiteverify(verifyURL, secret string) Verifier {
	return &siteverify{url: verifyURL, secret: secret, client: httpx.NewClient(5 * time.Second)}
}

func (s *siteverify) Verify(ctx context.Context, token, remoteIP string) error {
	if strings.TrimSpace(token) == "" {
		return ErrFailed
	}
	form := url.Values{"secret": {s.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("captcha provider unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("captcha provider answered %d", res.StatusCode)
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("captcha provider answer unreadable: %w", err)
	}
	if !out.Success {
		return ErrFailed
	}
	return nil
}
