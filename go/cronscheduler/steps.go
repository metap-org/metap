// TargetType "steps" chain execution — running a fresh chain (RunSteps), continuing one that
// paused at a wait_event step (ResumeSteps, called by trigger.go), and the shared step-range
// loop (runStepRange) both go through. Ported from metap-cron-scheduler's executor/steps.rs.
package cronscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/cron"
)

type activity struct {
	TargetType   string          `json:"targetType"`
	TargetConfig json.RawMessage `json:"targetConfig"`
}

type stepsConfig struct {
	Steps []activity `json:"steps"`
}

// RunSteps runs payload.TargetConfig.steps one after another, in order, within this single
// dispatch (until it either finishes, fails, or hits a wait_event step and pauses). Returns
// (summary, waiting, err) — waiting=true is a Steps-only outcome, left for the caller (dispatch
// in dispatch.go) to leave the run exactly as pause_workflow_run already left it, rather than
// calling finish_run_with_retry on top of a chain that is still durably paused.
func RunSteps(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, payload *cron.CronJobDuePayload) (json.RawMessage, bool, error) {
	var cfg stepsConfig
	if err := json.Unmarshal(payload.TargetConfig, &cfg); err != nil {
		return nil, false, err
	}
	if len(cfg.Steps) == 0 {
		return nil, false, fmt.Errorf("steps target_config.steps must not be empty")
	}

	workflowRunID, err := cron.StartWorkflowRun(ctx, pool, payload.TenantID, payload.JobID, payload.RunID, int32(len(cfg.Steps)))
	if err != nil {
		return nil, false, err
	}

	return runStepRange(ctx, pool, config, payload, workflowRunID, cfg.Steps, 0)
}

// runStepRange runs steps[startIndex:] in order against an already-existing workflowRunID —
// shared by RunSteps (a fresh chain, startIndex=0) and ResumeSteps (an already-paused chain
// picking back up right after its wait_event step). Pausing again on a second wait_event step
// later in the same chain works identically whether this is the chain's first run or a resume —
// this loop doesn't know or care which.
func runStepRange(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, payload *cron.CronJobDuePayload,
	workflowRunID uuid.UUID, steps []activity, startIndex int) (json.RawMessage, bool, error) {
	results := make([]json.RawMessage, 0, len(steps)-startIndex)
	for offset, step := range steps[startIndex:] {
		stepIndex := int32(startIndex + offset)

		if targetType, ok := cron.ParseTargetType(step.TargetType); ok && targetType == cron.TargetWaitEvent {
			var wait cron.WaitEventTargetConfig
			if err := json.Unmarshal(step.TargetConfig, &wait); err != nil {
				return nil, false, fmt.Errorf("step %d (wait_event) has an invalid targetConfig: %w", stepIndex, err)
			}
			if err := cron.PauseWorkflowRun(ctx, pool, workflowRunID, payload.RunID, stepIndex, wait); err != nil {
				return nil, false, err
			}
			slog.Info("workflow chain paused at wait_event step",
				"run_id", workflowRunID, "step_index", stepIndex, "entity", wait.Entity)
			return nil, true, nil
		}

		value, err := runOneStep(ctx, config, payload, step)
		if err != nil {
			message := fmt.Sprintf("step %d (%s) failed: %s", stepIndex, step.TargetType, err)
			if recordErr := cron.FailWorkflowRun(ctx, pool, workflowRunID, stepIndex, message); recordErr != nil {
				slog.Error("failed to record workflow run step failure",
					"run_id", workflowRunID, "step_index", stepIndex, "error", recordErr)
			}
			return nil, false, errors.New(message)
		}
		if err := cron.AdvanceWorkflowRun(ctx, pool, workflowRunID, stepIndex, value); err != nil {
			slog.Error("failed to record workflow run step progress",
				"run_id", workflowRunID, "step_index", stepIndex, "error", err)
		}
		results = append(results, value)
	}

	if err := cron.FinishWorkflowRun(ctx, pool, workflowRunID); err != nil {
		slog.Error("failed to mark workflow run finished", "run_id", workflowRunID, "error", err)
	}
	summary, err := json.Marshal(map[string]any{"steps": results})
	if err != nil {
		return nil, false, err
	}
	return summary, false, nil
}

// ResumeSteps continues a TargetType "steps" chain a wait_event step previously paused —
// trigger.go's listener calls this for every cron.ResumedWorkflowRun
// DispatchOnWaitEventTransitionMatches/DispatchOnWaitEventRecordMatches returns. On success,
// closes the run out directly (FinishRun, no retry needed). On failure, goes through
// FinishRunWithRetry exactly like a first-run failure — the chain retries from step 0 on the
// next attempt, not from wherever it paused; there's no resume-aware retry here, deliberately.
func ResumeSteps(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, resumed *cron.ResumedWorkflowRun) {
	payload := cron.CronJobDuePayload{
		RunID: resumed.CronJobRunID, JobID: resumed.JobID, TenantID: resumed.TenantID,
		TargetType: string(cron.TargetSteps), TargetConfig: resumed.TargetConfig,
		Attempt: resumed.Attempt, MaxAttempts: resumed.MaxAttempts, RetryBackoffSeconds: resumed.RetryBackoffSeconds,
		DispatchMode: resumed.DispatchMode, TriggerRecordID: &resumed.ResumingRecordID, TriggerEntity: &resumed.ResumingEntity,
	}

	var cfg stepsConfig
	if err := json.Unmarshal(payload.TargetConfig, &cfg); err != nil {
		slog.Error("failed to parse steps target_config on resume", "run_id", resumed.CronJobRunID, "error", err)
		msg := err.Error()
		if recordErr := cron.FinishRunWithRetry(ctx, pool, &payload, cron.RunFailed, &msg, nil); recordErr != nil {
			slog.Error("failed to record resumed cron job run result", "run_id", payload.RunID, "error", recordErr)
		}
		return
	}

	summary, waiting, err := runStepRange(ctx, pool, config, &payload, resumed.WorkflowRunID, cfg.Steps, int(resumed.ResumeFromStepIndex))
	switch {
	case err != nil:
		slog.Warn("resumed cron job chain failed", "job_id", payload.JobID, "run_id", payload.RunID, "error", err)
		msg := err.Error()
		if recordErr := cron.FinishRunWithRetry(ctx, pool, &payload, cron.RunFailed, &msg, nil); recordErr != nil {
			slog.Error("failed to record resumed cron job run result", "run_id", payload.RunID, "error", recordErr)
		}
	// Another wait_event step later in the same chain — already paused again by runStepRange/
	// PauseWorkflowRun, nothing left to do here.
	case waiting:
		slog.Info("cron job chain paused again on a later wait_event", "job_id", payload.JobID, "run_id", payload.RunID)
	default:
		slog.Info("cron job chain resumed and completed", "job_id", payload.JobID, "run_id", payload.RunID)
		if recordErr := cron.FinishRun(ctx, pool, payload.RunID, cron.RunSuccess, nil, summary); recordErr != nil {
			slog.Error("failed to record resumed cron job run result", "run_id", payload.RunID, "error", recordErr)
		}
	}
}

// runOneStep runs one step's activity, reusing the exact same Run* functions the non-chained
// target types dispatch through — a step is just an activity's (targetType, targetConfig) run
// in isolation. chain is the overall "steps" job's own CronJobDuePayload (not a per-step one —
// a step has no run_id/trigger context of its own): RunWebhook/RunEmail get the chain's real
// job_id/run_id/trigger fields, same as they'd see running as a standalone (non-chained) job for
// that same firing.
func runOneStep(ctx context.Context, config *ExecutorConfig, chain *cron.CronJobDuePayload, step activity) (json.RawMessage, error) {
	targetType, ok := cron.ParseTargetType(step.TargetType)
	if !ok {
		return nil, fmt.Errorf("unknown targetType %q in step", step.TargetType)
	}
	switch targetType {
	case cron.TargetWorkflowTransition:
		return RunWorkflowTransition(ctx, config.TargetGRPCBackend, step.TargetConfig)
	case cron.TargetBulkQueryAction:
		return RunBulkQueryAction(ctx, config.TargetGRPCBackend, step.TargetConfig)
	case cron.TargetWebhook:
		// The chain's own tenant, exactly as a standalone webhook job would use its row's — a
		// step inside a chain gets no different credential reach than one outside it.
		return RunWebhook(ctx, config.Webhook, config.Secrets, chain.TenantID, chain.JobID, chain.RunID,
			chain.TriggerEntity, chain.TriggerRecordID, step.TargetConfig)
	case cron.TargetEmail:
		return RunEmail(config.SMTP, chain.TriggerEntity, chain.TriggerRecordID, step.TargetConfig)
	case cron.TargetSteps:
		return nil, fmt.Errorf("nested \"steps\" targetType is not supported inside a step")
	case cron.TargetWaitEvent:
		// runStepRange intercepts wait_event steps before ever calling runOneStep — a step
		// reaching here as WaitEvent means that interception was skipped, a bug in the caller,
		// not a config error worth a normal "unsupported" message.
		return nil, fmt.Errorf("wait_event step reached runOneStep — should have been intercepted by runStepRange")
	}
	return nil, fmt.Errorf("unknown targetType %q in step", step.TargetType)
}
