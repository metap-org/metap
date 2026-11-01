// Package cronscheduler is the dispatch loop for the cron package's job definitions — ported
// from metap's Rust metap-cron-scheduler crate. Three loops run concurrently from
// cmd/cron-scheduler's main: RunTicker polls cron_jobs/cron_job_runs for schedule-due entries
// (and retries due from a prior failed attempt) and either writes a cron.job.due outbox event
// per firing (the default) or — for DispatchMode "direct" jobs — executes them immediately,
// in-process, in the same tick that claimed them; RunExecutor subscribes to that routing key and
// runs the job; RunTriggerListener subscribes to the catch-all exchange and fires
// trigger_type "on_transition"/"on_record_event" jobs whose trigger_config matches, resuming any
// wait_event-paused Steps chain that matches too. A fourth, unrelated housekeeping loop,
// RunOAuthCleanup, sweeps expired OAuth2 codes/refresh tokens — hosted here because this is the
// one always-on ops process, not because it has anything to do with cron jobs.
package cronscheduler

import (
	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/runtime"
)

// ExecutorConfig is everything the dispatch handlers need that isn't itself a job's own
// target_config (service token, callback base URL, SMTP settings).
type ExecutorConfig struct {
	// TargetBaseURL is the base URL of the owning app instance's POST /auth/login ("REST" stays
	// for /auth/* even though the entity-CRUD surface it used to also cover is gone — see
	// TargetGRPCBackend's doc comment).
	TargetBaseURL string

	// TargetGRPCBackend is the workflow_transition/bulk_query_action jobs' entity CRUD backend —
	// metap-http no longer serves a REST /api/:entity* surface for these to call, so this is a
	// grpcapi.GrpcBackend connected to the owning app's RecordService gRPC port instead. nil when
	// CRON_TARGET_GRPC_ADDR is unset or the initial connection failed — same "most job types
	// don't need this, only these two fail" degrade-gracefully shape as ServiceToken/Secrets
	// below, not a boot failure.
	TargetGRPCBackend crud.RecordBackend

	// ServiceToken is a service-account token this process logged into the owning app's own
	// POST /auth/login with, kept fresh in the background. Its tenantId claim fixes which
	// tenant's jobs this executor can actually run.
	ServiceToken *runtime.ServiceTokenSource

	// SMTP settings for TargetType "email" jobs (RunEmail).
	SMTP SmtpConfig

	// Webhook guards TargetType "webhook"'s tenant-supplied target URL — see ssrf_guard.go.
	Webhook WebhookPolicy

	// Secrets resolves a tenant's stored webhook credential — the same SecretStore backend every
	// other process picks via control.BuildSecretStore, so a deployment on Vault does not find
	// this one reading somewhere else. nil means no credential can be resolved and a job that
	// asks for one fails saying so — the honest failure, rather than silently sending the webhook
	// without the Authorization it was configured to carry.
	Secrets control.SecretStore
}

type SmtpConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}
