// The polling half: claims due jobs/retries and either writes their cron.job.due outbox event (the
// default) or — for DispatchMode "direct" jobs — starts them in-process as soon as the tick has
// claimed them.
// Go's context cancellation doesn't abort an in-flight pgx query the same structural way — this
// loop instead lets an in-flight tick run to completion (pgx itself still respects ctx cancellation
// inside its own queries, so a long-stuck query is still interrupted) and checks ctx.Err()
// before/after each tick, which is a close equivalent rather than byte-identical.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/jobs/cron"
	"github.com/metap-org/metap/pkg/kit/standard"
)

type TickerConfig struct {
	Interval  time.Duration
	BatchSize int64
}

// RunTicker runs the poll loop until ctx is canceled.
func RunTicker(ctx context.Context, pool *pgxpool.Pool, executorConfig *ExecutorConfig, config TickerConfig) error {
	direct := newBackground(executorConfig)
	defer direct.wait()
	for {
		if ctx.Err() != nil {
			slog.Info("shutdown signal received, exiting ticker")
			return nil
		}

		if err := tick(ctx, pool, direct, config.BatchSize); err != nil {
			if ctx.Err() != nil {
				slog.Info("shutdown signal received, exiting ticker")
				return nil
			}
			return err
		}

		select {
		case <-ctx.Done():
			slog.Info("shutdown signal received, exiting ticker")
			return nil
		case <-time.After(config.Interval):
		}
	}
}

func tick(ctx context.Context, pool *pgxpool.Pool, direct *background, batchSize int64) error {
	due, err := cron.ClaimDueJobs(ctx, pool, time.Now().UTC(), batchSize)
	if err != nil {
		return err
	}
	if due.Claimed > 0 {
		slog.Info("cron ticker claimed due jobs", "claimed", due.Claimed, "direct", len(due.DirectJobs))
	}
	runDirectJobs(ctx, pool, direct, due.DirectJobs)

	// Retries scheduled by a prior failed attempt (FinishRunWithRetry) — same claim/dispatch
	// shape as ClaimDueJobs, just sourced from cron_job_runs instead of cron_jobs.
	retries, err := cron.ClaimDueRetries(ctx, pool, time.Now().UTC(), batchSize)
	if err != nil {
		return err
	}
	if retries.Claimed > 0 {
		slog.Info("cron ticker claimed due retries", "claimed", retries.Claimed, "direct", len(retries.DirectJobs))
	}
	runDirectJobs(ctx, pool, direct, retries.DirectJobs)

	return nil
}

// runDirectJobs starts every claimed Direct-mode job in the background, so a slow one holds up
// neither the next tick nor the trigger consumer. Direct stays fire-and-forget: no message stands
// behind the run, so a first attempt cut off by a crash is not run again (a retry attempt is, by
// ClaimDueRetries). A job that must not be lost uses DispatchMode "outbox".
func runDirectJobs(ctx context.Context, pool *pgxpool.Pool, direct *background, directJobs []cron.ClaimedDirectJob) {
	for _, dj := range directJobs {
		payload := cron.CronJobDuePayload{
			RunID: dj.RunID, JobID: dj.JobID, TenantID: dj.TenantID, TargetType: dj.TargetType,
			TargetConfig: dj.TargetConfig, Attempt: dj.Attempt, MaxAttempts: dj.MaxAttempts,
			RetryBackoffSeconds: dj.RetryBackoffSeconds, DispatchMode: dj.DispatchMode,
			TriggerRecordID: dj.TriggerRecordID, TriggerEntity: dj.TriggerEntity,
		}
		if !direct.run(ctx, func() { Execute(ctx, pool, direct.config, &payload) }) {
			slog.Warn("shutting down before a direct job could start", standard.LogRunID, payload.RunID, standard.LogJobID, payload.JobID)
		}
	}
}
