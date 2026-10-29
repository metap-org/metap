package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"
)

// How often the background loop logs back in, as a fraction of the server's own token TTL
// (metap-http's TOKEN_TTL_SECONDS, 3600s — keep in sync if either changes). Leaves a 20-minute
// buffer for the login endpoint or the network to be down for a while without the cached token
// actually expiring.
const serviceTokenRefreshInterval = 2400 * time.Second

// Backoff before retrying a failed background login.
const serviceTokenRetryBackoff = 30 * time.Second

// How long the *initial* login may keep retrying a transient connection failure before failing
// the caller's boot sequence for real.
const serviceTokenInitialRetryBudget = 60 * time.Second
const serviceTokenInitialRetryInterval = 2 * time.Second

// ServiceTokenSource is a bearer token for one process's own service account, obtained via a
// POST <loginURL> call and kept fresh by a background goroutine — ported from metap-runtime's
// ServiceTokenSource. Cheap to read (Current() is a lock-free atomic load); logging in again
// happens only on the background timer or a transient-failure retry, never on the request path.
type ServiceTokenSource struct {
	current atomic.Pointer[string]
}

type loginOutcomeKind int

const (
	outcomeOK loginOutcomeKind = iota
	outcomeRetryable
	outcomeFatal
)

type loginOutcome struct {
	kind  loginOutcomeKind
	token string
	err   error
}

// StartServiceTokenSource logs in, retrying a transient connection failure for up to
// serviceTokenInitialRetryBudget — but failing immediately on a definite answer from the
// upstream (bad credentials, a real 5xx, an unparseable response). Then spawns the background
// refresh loop, which runs until ctx is canceled.
func StartServiceTokenSource(ctx context.Context, httpClient *http.Client, loginURL, email, password string) (*ServiceTokenSource, error) {
	deadline := time.Now().Add(serviceTokenInitialRetryBudget)
	var token string
	for {
		outcome := loginOnce(ctx, httpClient, loginURL, email, password)
		switch outcome.kind {
		case outcomeOK:
			token = outcome.token
		case outcomeFatal:
			return nil, fmt.Errorf("logging into %s as %s: %w", loginURL, email, outcome.err)
		case outcomeRetryable:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("logging into %s as %s (gave up after %s of retries): %w",
					loginURL, email, serviceTokenInitialRetryBudget, outcome.err)
			}
			slog.Warn("upstream not reachable yet, retrying", "login_url", loginURL, "error", outcome.err,
				"retry_in", serviceTokenInitialRetryInterval)
			time.Sleep(serviceTokenInitialRetryInterval)
			continue
		}
		break
	}

	s := &ServiceTokenSource{}
	s.current.Store(&token)

	go func() {
		// nextDelay carries the previous attempt's outcome into the next sleep — a failed login
		// must retry after serviceTokenRetryBackoff, not after another full refresh interval on
		// top of it (the exact bug this Rust port preserves the fix for).
		nextDelay := serviceTokenRefreshInterval
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(nextDelay):
			}
			outcome := loginOnce(ctx, httpClient, loginURL, email, password)
			switch outcome.kind {
			case outcomeOK:
				tok := outcome.token
				s.current.Store(&tok)
				nextDelay = serviceTokenRefreshInterval
				slog.Info("refreshed service account token", "login_url", loginURL, "next_refresh", serviceTokenRefreshInterval)
			default:
				nextDelay = serviceTokenRetryBackoff
				slog.Error("failed to refresh service account token, keeping previous one",
					"login_url", loginURL, "error", outcome.err, "retry_in", serviceTokenRetryBackoff)
			}
		}
	}()

	return s, nil
}

// NewStaticServiceTokenSource returns a fixed, never-refreshing token — for tests with no real
// login server to call.
func NewStaticServiceTokenSource(token string) *ServiceTokenSource {
	s := &ServiceTokenSource{}
	s.current.Store(&token)
	return s
}

// Current returns the most recently obtained token.
func (s *ServiceTokenSource) Current() string {
	return *s.current.Load()
}

type loginResponseEnvelope struct {
	Data struct {
		Token string `json:"token"`
	} `json:"data"`
}

func isRetryableNetError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

func loginOnce(ctx context.Context, httpClient *http.Client, loginURL, email, password string) loginOutcome {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(body))
	if err != nil {
		return loginOutcome{kind: outcomeFatal, err: fmt.Errorf("building request to %s: %w", loginURL, err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		// No response at all — the upstream isn't there to have answered, which is exactly the
		// boot-race case worth retrying. Anything else is a real configuration bug, not a race.
		wrapped := fmt.Errorf("POST %s: %w", loginURL, err)
		if isRetryableNetError(err) {
			return loginOutcome{kind: outcomeRetryable, err: wrapped}
		}
		return loginOutcome{kind: outcomeFatal, err: wrapped}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return loginOutcome{kind: outcomeFatal, err: fmt.Errorf("%s returned status %d", loginURL, resp.StatusCode)}
	}

	var envelope loginResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return loginOutcome{kind: outcomeFatal, err: fmt.Errorf("parsing %s response: %w", loginURL, err)}
	}
	return loginOutcome{kind: outcomeOK, token: envelope.Data.Token}
}
