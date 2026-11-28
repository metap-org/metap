-- Who is running a firing right now. A run is claimed by setting a token with an expiry in one
-- conditional UPDATE; the worker pushes the expiry forward while it works, and only the holder of the
-- token records the result. A worker that dies stops renewing, so the run can be claimed again.
-- Both are NULL for a run nobody holds. The status stays 'enqueued' while a run is held.
ALTER TABLE metadata.cron_job_runs ADD COLUMN lease_token uuid;
ALTER TABLE metadata.cron_job_runs ADD COLUMN lease_expires_at timestamptz;
