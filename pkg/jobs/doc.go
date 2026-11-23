// Package jobs is the group of everything that runs work on a schedule or on an event. It has no code
// of its own; the two packages below are one feature split by what they need:
//
//   - jobs/cron: the job definitions and their state. Job and run records, schedule math
//     (cron expressions, time zones), the claim of what is due, retries, workflow (Steps) runs. Plain
//     storage and arithmetic, no process loop, safe to import from an HTTP route or an admin service.
//
//   - jobs/scheduler: the process that executes them. The ticker, the executor, the trigger
//     listener, the job targets (webhook with its SSRF guard, email, workflow transition, bulk query
//     action, Steps) and the housekeeping loops hosted in the same always-on process (tenant purge, audit
//     partition maintenance, OAuth2 token cleanup). Imports cron, never the other way round.
//
// The binary that runs the scheduler is cmd/cron-scheduler.
package jobs
