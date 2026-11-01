-- Increment 3 (`docs/features/02-workflow-engine.md`): `TargetType::WaitEvent` durable pause.
-- `status` gains a `"waiting"` value (no CHECK constraint on the column, same posture as
-- `cron_job_runs.status`/`workflow_runs.status` already have — validated in Rust, not SQL).
ALTER TABLE "workflow_runs" ADD COLUMN "wait_entity" text;
--> statement-breakpoint
ALTER TABLE "workflow_runs" ADD COLUMN "wait_action" text;
--> statement-breakpoint
ALTER TABLE "workflow_runs" ADD COLUMN "wait_record_event" text;
--> statement-breakpoint
-- Two partial indexes rather than one — a waiting row has exactly one of `wait_action`/
-- `wait_record_event` set (mirrors `WaitEventTargetConfig`), and `cron-scheduler::trigger`'s
-- listener always knows up front which of the two it's matching (a `.workflow.transitioned`
-- topic vs a `.record.{created,updated,deleted}` one), so each index only ever serves one query
-- shape.
CREATE INDEX "workflow_runs_waiting_transition_idx" ON "workflow_runs" USING btree ("tenant_id", "wait_entity", "wait_action") WHERE "status" = 'waiting' AND "wait_action" IS NOT NULL;
--> statement-breakpoint
CREATE INDEX "workflow_runs_waiting_record_event_idx" ON "workflow_runs" USING btree ("tenant_id", "wait_entity", "wait_record_event") WHERE "status" = 'waiting' AND "wait_record_event" IS NOT NULL;
