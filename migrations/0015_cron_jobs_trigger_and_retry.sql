ALTER TABLE "cron_jobs" ADD COLUMN "trigger_type" varchar(20) DEFAULT 'schedule' NOT NULL;
ALTER TABLE "cron_jobs" ADD COLUMN "trigger_config" jsonb;
ALTER TABLE "cron_jobs" ALTER COLUMN "cron_expr" DROP NOT NULL;
ALTER TABLE "cron_jobs" ALTER COLUMN "next_run_at" DROP NOT NULL;
ALTER TABLE "cron_jobs" ADD COLUMN "max_attempts" integer DEFAULT 1 NOT NULL;
ALTER TABLE "cron_jobs" ADD COLUMN "retry_backoff_seconds" integer DEFAULT 30 NOT NULL;
--> statement-breakpoint
ALTER TABLE "cron_job_runs" ADD COLUMN "attempt" integer DEFAULT 1 NOT NULL;
--> statement-breakpoint
CREATE INDEX "cron_jobs_on_transition_idx" ON "cron_jobs" USING btree ("tenant_id", "trigger_type", "enabled") WHERE "trigger_type" = 'on_transition';
--> statement-breakpoint
CREATE INDEX "cron_job_runs_pending_retry_idx" ON "cron_job_runs" USING btree ("status", "scheduled_for") WHERE "attempt" > 1;
