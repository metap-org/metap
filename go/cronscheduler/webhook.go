// "webhook" targets — calls an external URL, injecting jobId/runId (always) and entity/recordId
// (when the firing came from an on_transition/on_record_event trigger — a plain schedule job has
// neither) into a JSON body. Ported from metap-cron-scheduler's executor/webhook.rs.
//
// The URL, method, headers and body all come from a tenant admin, so every one of them goes
// through ssrfguard.go first.
//
// Uses its own *http.Client, not a shared one, for one reason the guard can't cover on its own:
// redirects. An allowed public host answering "302 Location: http://169.254.169.254/" would walk
// straight past a check that only ever saw the first URL. CheckRedirect returning
// http.ErrUseLastResponse makes the first hop the only hop — a webhook that legitimately needs
// to follow one now fails visibly with the 3xx status rather than silently becoming an SSRF
// primitive.
package cronscheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/runtime"
)

// webhookAuthorizationKey is the config key whose credential a webhook may present
// (metap-config's keys::CRON_WEBHOOK_AUTHORIZATION). Spelled as a literal rather than imported
// from a config package so this stays free of that dependency — it never reads tenant_configs at
// all, since the reference is derived from (tenantID, key), not looked up from a row.
const webhookAuthorizationKey = "cron.webhookAuthorization"

type webhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	// AuthorizationFromSecret: opt-in, a boolean deliberately — not a reference string. A tenant
	// naming the secret to use would be the exact hole control.TenantSecretRef exists to remove:
	// the reference is derived from the job's own tenantID, so a job can only ever present its
	// own tenant's credential, whatever it puts in target_config.
	AuthorizationFromSecret bool `json:"authorizationFromSecret,omitempty"`
}

var (
	webhookClientOnce sync.Once
	webhookClient     *http.Client
)

func webhookHTTPClient() *http.Client {
	webhookClientOnce.Do(func() {
		webhookClient = &http.Client{
			Timeout: runtime.DefaultTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})
	return webhookClient
}

// RunWebhook runs one "webhook" target firing (or step).
func RunWebhook(ctx context.Context, policy WebhookPolicy, secrets control.SecretStore, tenantID, jobID, runID uuid.UUID,
	triggerEntity *string, triggerRecordID *uuid.UUID, targetConfig json.RawMessage) (json.RawMessage, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, err
	}
	method := cfg.Method
	if method == "" {
		method = "POST"
	}

	headerNames := make([]string, 0, len(cfg.Headers))
	for k := range cfg.Headers {
		headerNames = append(headerNames, k)
	}
	// Both checks run before any network call: a rejected job fails with a reason the operator
	// can read in cron_job_runs.error, having sent nothing.
	if err := CheckHeaders(headerNames); err != nil {
		return nil, err
	}
	u, err := policy.Check(ctx, cfg.URL)
	if err != nil {
		return nil, err
	}

	// Only now. The credential is read after the target survived the scheme, IP and allowlist
	// screens, so a secret can never be sent to an address the guard would have rejected.
	// Reversing this ordering would turn this feature back into the credential-forgery primitive
	// audit 04 A#1 closed.
	var credential string
	hasCredential := false
	if cfg.AuthorizationFromSecret {
		if secrets == nil {
			return nil, fmt.Errorf("this job requests its tenant's stored credential, but no secret backend is configured for cron-scheduler")
		}
		secretRef := control.TenantSecretRef(tenantID, webhookAuthorizationKey)
		value, serr := secrets.GetSecret(ctx, secretRef)
		if serr != nil {
			// The reference is safe to name (it is derivable from the tenant id); the value is
			// not, and never reaches this string.
			return nil, fmt.Errorf("could not resolve this tenant's webhook credential (%s): %w", secretRef, serr)
		}
		credential, hasCredential = value, true
	}

	var bodyValue any = map[string]any{}
	if len(cfg.Body) > 0 {
		if err := json.Unmarshal(cfg.Body, &bodyValue); err != nil {
			return nil, fmt.Errorf("invalid webhook body: %w", err)
		}
	}
	if obj, ok := bodyValue.(map[string]any); ok {
		obj["jobId"] = jobID.String()
		obj["runId"] = runID.String()
		// Only present for an on_transition/on_record_event-triggered firing — a plain schedule
		// job has neither.
		if triggerEntity != nil {
			obj["entity"] = *triggerEntity
		}
		if triggerRecordID != nil {
			obj["recordId"] = triggerRecordID.String()
		}
		bodyValue = obj
	}
	bodyBytes, err := json.Marshal(bodyValue)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP method %q: %w", cfg.Method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}
	// Set last, so a tenant header map can never shadow it — CheckHeaders already refuses a
	// literal authorization, and this ordering means a future loosening there still could not
	// replace the credential with a chosen value.
	if hasCredential {
		req.Header.Set("Authorization", credential)
	}
	runtime.AttachTraceContext(ctx, req)

	resp, err := webhookHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyText, _ := io.ReadAll(resp.Body)
	// Everything below reaches cron_job_runs, which the tenant admin reads back. An upstream that
	// echoes the request would otherwise hand the credential straight back through that surface.
	text := redact(string(bodyText), credential, hasCredential)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("webhook returned %d: %s", resp.StatusCode, truncate(text, 500))
	}
	return json.Marshal(map[string]any{"status": resp.StatusCode, "body": truncate(text, 2000)})
}

// redact replaces every occurrence of the credential just sent with a marker — belt and braces
// rather than the primary defense, but cron_job_runs is read by the same tenant admin who can
// set the webhook URL, so "point the job at a server that echoes headers" is a one-step way to
// read a credential back that the write-only config surface deliberately does not offer.
//
// Also covers the empty-secret case explicitly: replacing every empty substring would otherwise
// shred the whole body.
func redact(text, secret string, hasCredential bool) string {
	if hasCredential && secret != "" {
		return strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}

// truncate truncates on a rune boundary, not a byte offset — the response body is from a server
// the tenant chose, so a webhook answering with UTF-8 text straddling the byte limit must not
// produce an invalid string.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := 0
	for i := range s {
		if i > max {
			break
		}
		end = i
	}
	return s[:end] + "..."
}
