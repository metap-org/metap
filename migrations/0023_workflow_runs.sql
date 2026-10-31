CREATE TABLE "workflow_runs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"tenant_id" uuid NOT NULL,
	"job_id" uuid NOT NULL REFERENCES "cron_jobs"("id") ON DELETE CASCADE,
	"cron_job_run_id" uuid NOT NULL REFERENCES "cron_job_runs"("id") ON DELETE CASCADE,
	"status" varchar(20) DEFAULT 'running' NOT NULL,
	"current_step_index" integer DEFAULT 0 NOT NULL,
	"total_steps" integer NOT NULL,
	"context" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"error" text,
	"started_at" timestamp with time zone DEFAULT now() NOT NULL,
	"finished_at" timestamp with time zone,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX "workflow_runs_cron_job_run_idx" ON "workflow_runs" USING btree ("cron_job_run_id");--> statement-breakpoint
CREATE INDEX "workflow_runs_tenant_job_idx" ON "workflow_runs" USING btree ("tenant_id","job_id","created_at");
