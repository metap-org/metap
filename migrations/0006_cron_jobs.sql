CREATE TABLE "cron_jobs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"tenant_id" uuid NOT NULL,
	"name" varchar(200) NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"cron_expr" varchar(120) NOT NULL,
	"timezone" varchar(80) DEFAULT 'UTC' NOT NULL,
	"target_type" varchar(40) NOT NULL,
	"target_config" jsonb NOT NULL,
	"next_run_at" timestamp with time zone NOT NULL,
	"last_run_at" timestamp with time zone,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL,
	"created_by" uuid
);
--> statement-breakpoint
CREATE TABLE "cron_job_runs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"tenant_id" uuid NOT NULL,
	"job_id" uuid NOT NULL REFERENCES "cron_jobs"("id") ON DELETE CASCADE,
	"status" varchar(20) NOT NULL,
	"scheduled_for" timestamp with time zone NOT NULL,
	"started_at" timestamp with time zone,
	"finished_at" timestamp with time zone,
	"error" text,
	"response_summary" jsonb,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE INDEX "cron_jobs_due_idx" ON "cron_jobs" USING btree ("enabled","next_run_at");--> statement-breakpoint
CREATE INDEX "cron_jobs_tenant_idx" ON "cron_jobs" USING btree ("tenant_id");--> statement-breakpoint
CREATE INDEX "cron_job_runs_job_created_idx" ON "cron_job_runs" USING btree ("job_id","created_at");--> statement-breakpoint
CREATE INDEX "cron_job_runs_tenant_idx" ON "cron_job_runs" USING btree ("tenant_id","created_at");
