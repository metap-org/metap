-- A trigger fires a job once per event, however many times the event is delivered. The broker is
-- at-least-once (a worker that dies between committing the firing and acking, a failed ack, an outbox
-- batch republished after a failed commit), so each firing records the id of the event that caused it
-- and a second delivery of the same event finds the run already there instead of starting another.
-- NULL for a scheduled or retried run, which no event started.
ALTER TABLE metadata.cron_job_runs ADD COLUMN trigger_event_id uuid;

CREATE UNIQUE INDEX cron_job_runs_trigger_event_uniq
  ON metadata.cron_job_runs (job_id, trigger_event_id)
  WHERE trigger_event_id IS NOT NULL;
