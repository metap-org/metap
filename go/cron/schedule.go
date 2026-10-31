// Package cron is metadata-driven scheduled jobs — ported from metap's Rust metap-cron crate.
// An operator defines a recurring job (schedule + target action) through the admin API; this
// package owns the job definition/run-history storage and due-job-claiming logic. The actual
// dispatch loop (ticker + executor) lives in a separate ops binary (ported in a later wave).
package cron

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ValidateSchedule validates cronExpr (6-field: sec min hour day-of-month month day-of-week,
// e.g. "0 */5 * * * *" for every 5 minutes) and timezone (an IANA name) without computing an
// occurrence.
func ValidateSchedule(cronExpr, timezone string) error {
	if _, err := cronParser.Parse(cronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("invalid IANA timezone %q", timezone)
	}
	return nil
}

// NextRunAt is the next occurrence of cronExpr strictly after after, evaluated in timezone (so
// "every day at 9am" means 9am local time, not UTC) and returned back in UTC for storage.
func NextRunAt(cronExpr, timezone string, after time.Time) (time.Time, error) {
	schedule, err := cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid IANA timezone %q", timezone)
	}

	afterInTZ := after.In(loc)
	next := schedule.Next(afterInTZ)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q has no future occurrence", cronExpr)
	}
	return next.UTC(), nil
}
