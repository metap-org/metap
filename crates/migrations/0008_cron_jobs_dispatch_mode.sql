ALTER TABLE "cron_jobs" ADD COLUMN "dispatch_mode" varchar(20) DEFAULT 'outbox' NOT NULL;
