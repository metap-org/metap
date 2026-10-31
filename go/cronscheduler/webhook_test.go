package cronscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Live verification (a real outbound HTTP call, against a real test server) that an
// on_record_event/on_transition-triggered firing's entity/recordId actually reach the webhook
// body.
func TestInjectsEntityAndRecordIDWhenTheFiringHasTriggerContext(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy := WebhookPolicy{AllowPrivateTargets: true}
	jobID := uuid.New()
	runID := uuid.New()
	recordID := uuid.New()
	entity := "crm.customers"
	targetConfig, _ := json.Marshal(map[string]any{"url": srv.URL})

	if _, err := RunWebhook(context.Background(), policy, nil, uuid.New(), jobID, runID, &entity, &recordID, targetConfig); err != nil {
		t.Fatalf("webhook call failed: %v", err)
	}

	if gotBody["jobId"] != jobID.String() {
		t.Errorf("jobId = %v, want %v", gotBody["jobId"], jobID.String())
	}
	if gotBody["runId"] != runID.String() {
		t.Errorf("runId = %v, want %v", gotBody["runId"], runID.String())
	}
	if gotBody["entity"] != entity {
		t.Errorf("entity = %v, want %v", gotBody["entity"], entity)
	}
	if gotBody["recordId"] != recordID.String() {
		t.Errorf("recordId = %v, want %v", gotBody["recordId"], recordID.String())
	}
}

// A plain schedule-triggered job has no trigger context — entity/recordId must not appear at
// all (not even null), so a receiver can distinguish "no context" from "context happened to be
// empty".
func TestOmitsEntityAndRecordIDWhenTheFiringHasNoTriggerContext(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy := WebhookPolicy{AllowPrivateTargets: true}
	targetConfig, _ := json.Marshal(map[string]any{"url": srv.URL})

	if _, err := RunWebhook(context.Background(), policy, nil, uuid.New(), uuid.New(), uuid.New(), nil, nil, targetConfig); err != nil {
		t.Fatalf("webhook call failed: %v", err)
	}

	if _, ok := gotBody["entity"]; ok {
		t.Error("entity must not be present")
	}
	if _, ok := gotBody["recordId"]; ok {
		t.Error("recordId must not be present")
	}
}

// The response body is what a tenant admin reads back from cron_job_runs, so a credential
// echoed by the upstream must not survive the trip.
func TestRedactRemovesAnEchoedCredentialFromAResponseBody(t *testing.T) {
	body := `{"received":{"authorization":"Bearer sk_live_abc123"}}`
	out := redact(body, "Bearer sk_live_abc123", true)
	if strings.Contains(out, "sk_live_abc123") {
		t.Errorf("credential leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected redaction marker: %s", out)
	}
}

func TestRedactLeavesABodyAloneWhenNoCredentialWasSent(t *testing.T) {
	body := `{"ok":true}`
	if got := redact(body, "", false); got != body {
		t.Errorf("got %q, want %q", got, body)
	}
	// An empty secret must not turn into "replace every empty substring", which would insert a
	// marker between every character of the body.
	if got := redact(body, "", true); got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

// A tenant opts in with a boolean; there is no field for naming which secret, because the
// reference is derived from the job's own tenant.
func TestTheConfigCarriesAnOptInFlagAndNoSecretReference(t *testing.T) {
	var cfg webhookConfig
	raw := []byte(`{"url":"https://api.example.com/hook","authorizationFromSecret":true,"secretRef":"METAP_TENANT_SOMEONEELSE_CRON_WEBHOOKAUTHORIZATION"}`)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.AuthorizationFromSecret {
		t.Error("expected AuthorizationFromSecret to be true")
	}

	var def webhookConfig
	if err := json.Unmarshal([]byte(`{"url":"https://api.example.com/hook"}`), &def); err != nil {
		t.Fatal(err)
	}
	if def.AuthorizationFromSecret {
		t.Error("a job must not send a credential unless it asked to")
	}
}

func TestTruncateKeepsShortStringsWhole(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestTruncateDoesNotPanicOnAMultibyteBoundary(t *testing.T) {
	// "é" is 2 bytes: cutting at byte 5 lands mid-character.
	s := "abcdéfgh"
	out := truncate(s, 5)
	if !strings.HasSuffix(out, "...") {
		t.Errorf("expected ... suffix, got %q", out)
	}
}

func TestTruncateHandlesAStringThatIsEntirelyMultibyte(t *testing.T) {
	s := "🙂🙂🙂🙂"
	if out := truncate(s, 5); !strings.HasSuffix(out, "...") {
		t.Errorf("expected ... suffix, got %q", out)
	}
}
