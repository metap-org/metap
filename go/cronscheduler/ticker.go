// The polling half: claims due jobs/retries and either writes their cron.job.due outbox event
// (the default) or — for DispatchMode "direct" jobs — executes them immediately, in-process, in
// the same tick that claimed them. Ported from metap-cron-scheduler's ticker.rs.
//
// Divergence from the Rust original: Rust's loop races a biased tokio::select! against shutdown
// around the tick() call itself, so a shutdown mid-tick aborts the in-flight future outright.
// Go's context cancellation doesn't abort an in-flight pgx query the same structural way — this
// loop instead lets an in-flight tick run to completion (pgx itself still respects ctx
// cancellation inside its own queries, so a long-stuck query is still interrupted) and checks
// ctx.Err() before/after each tick, which is a close equivalent rather than byte-identical.
package cronscheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/cron"
)

type TickerConfig struct {
	Interval  time.Duration
	BatchSize int64
}

// RunTicker runs the poll loop until ctx is canceled.
func RunTicker(ctx context.Context, pool *pgxpool.Pool, executorConfig *ExecutorConfig, config TickerConfig) error {
	for {
		if ctx.Err() != nil {
			slog.Info("shutdown signal received, exiting ticker")
			return nil
		}

		if err := tick(ctx, pool, executorConfig, config.BatchSize); err != nil {
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

func tick(ctx context.Context, pool *pgxpool.Pool, executorConfig *ExecutorConfig, batchSize int64) error {
	due, err := cron.ClaimDueJobs(ctx, pool, time.Now().UTC(), batchSize)
	if err != nil {
		return err
	}
	if due.Claimed > 0 {
		slog.Info("cron ticker claimed due jobs", "claimed", due.Claimed, "direct", len(due.DirectJobs))
	}
	runDirectJobs(ctx, pool, executorConfig, due.DirectJobs)

	// Retries scheduled by a prior failed attempt (FinishRunWithRetry) — same claim/dispatch
	// shape as ClaimDueJobs, just sourced from cron_job_runs instead of cron_jobs.
	retries, err := cron.ClaimDueRetries(ctx, pool, time.Now().UTC(), batchSize)
	if err != nil {
		return err
	}
	if retries.Claimed > 0 {
		slog.Info("cron ticker claimed due retries", "claimed", retries.Claimed, "direct", len(retries.DirectJobs))
	}
	runDirectJobs(ctx, pool, executorConfig, retries.DirectJobs)

	return nil
}

// runDirectJobs runs every Direct-mode job claimed this tick right here, sequentially — a slow
// direct job delays the next tick's claim, which is the fire-and-forget tradeoff this dispatch
// mode signs up for; a job that can't tolerate that delay should use DispatchMode "outbox"
// instead.
func runDirectJobs(ctx context.Context, pool *pgxpool.Pool, executorConfig *ExecutorConfig, directJobs []cron.ClaimedDirectJob) {
	for _, dj := range directJobs {
		payload := cron.CronJobDuePayload{
			RunID: dj.RunID, JobID: dj.JobID, TenantID: dj.TenantID, TargetType: dj.TargetType,
			TargetConfig: dj.TargetConfig, Attempt: dj.Attempt, MaxAttempts: dj.MaxAttempts,
			RetryBackoffSeconds: dj.RetryBackoffSeconds, DispatchMode: dj.DispatchMode,
			TriggerRecordID: dj.TriggerRecordID, TriggerEntity: dj.TriggerEntity,
		}
		Execute(ctx, pool, executorConfig, &payload)
	}
}
