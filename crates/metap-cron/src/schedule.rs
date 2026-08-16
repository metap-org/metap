//! Cron-expression + IANA-timezone occurrence math, kept separate from `store.rs` so it has
//! no database dependency and is trivially unit-testable.

use std::str::FromStr;

use chrono::{DateTime, Utc};
use chrono_tz::Tz;
use cron::Schedule;

/// Validates `cron_expr` (standard `cron` crate 6-field syntax: `sec min hour day-of-month
/// month day-of-week`, e.g. `0 */5 * * * *` for every 5 minutes) and `timezone` (an IANA
/// name, e.g. `Asia/Ho_Chi_Minh`) without computing an occurrence — used by the admin
/// create/update handlers to reject a bad job definition at write time instead of failing
/// silently the first time the ticker tries to schedule it.
pub fn validate(cron_expr: &str, timezone: &str) -> anyhow::Result<()> {
    Schedule::from_str(cron_expr).map_err(|e| anyhow::anyhow!("invalid cron expression {cron_expr:?}: {e}"))?;
    timezone
        .parse::<Tz>()
        .map_err(|_| anyhow::anyhow!("invalid IANA timezone {timezone:?}"))?;
    Ok(())
}

/// The next occurrence of `cron_expr` strictly after `after`, evaluated in `timezone` (so
/// "every day at 9am" means 9am local time, not UTC) and returned back in UTC for storage.
pub fn next_run_at(cron_expr: &str, timezone: &str, after: DateTime<Utc>) -> anyhow::Result<DateTime<Utc>> {
    let schedule =
        Schedule::from_str(cron_expr).map_err(|e| anyhow::anyhow!("invalid cron expression {cron_expr:?}: {e}"))?;
    let tz: Tz = timezone
        .parse()
        .map_err(|_| anyhow::anyhow!("invalid IANA timezone {timezone:?}"))?;

    let after_in_tz = after.with_timezone(&tz);
    let next = schedule
        .after(&after_in_tz)
        .next()
        .ok_or_else(|| anyhow::anyhow!("cron expression {cron_expr:?} has no future occurrence"))?;
    Ok(next.with_timezone(&Utc))
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn every_five_minutes_utc() {
        let after = Utc.with_ymd_and_hms(2026, 1, 1, 0, 1, 0).unwrap();
        let next = next_run_at("0 */5 * * * *", "UTC", after).unwrap();
        assert_eq!(next, Utc.with_ymd_and_hms(2026, 1, 1, 0, 5, 0).unwrap());
    }

    #[test]
    fn daily_9am_respects_timezone_offset() {
        // 9am in Asia/Ho_Chi_Minh (UTC+7) is 2am UTC.
        let after = Utc.with_ymd_and_hms(2026, 1, 1, 0, 0, 0).unwrap();
        let next = next_run_at("0 0 9 * * *", "Asia/Ho_Chi_Minh", after).unwrap();
        assert_eq!(next, Utc.with_ymd_and_hms(2026, 1, 1, 2, 0, 0).unwrap());
    }

    #[test]
    fn rejects_bad_expression() {
        assert!(validate("not a cron expr", "UTC").is_err());
    }

    #[test]
    fn rejects_bad_timezone() {
        assert!(validate("0 */5 * * * *", "Not/A_Zone").is_err());
    }
}
