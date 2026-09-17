use chrono::{DateTime, Utc};
use serde_json::{Map, Value};
use uuid::Uuid;

/// The record's `data jsonb` blob shape — same type `metap-crud`'s own `JsonObject` alias points
/// at, duplicated here (not imported from `metap-crud`) since `metap-crud` depends on this crate,
/// not the other way around.
pub type JsonObject = Map<String, Value>;

/// Which of `CrudService`'s 4 writes produced this entry. `Transition` carries the workflow
/// action name separately (`AuditEntry::transition_action`) — `workflow_events` already owns
/// `from_state`/`to_state` for that narrower purpose, this enum only distinguishes the 4 write
/// kinds an audit trail cares about.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum AuditAction {
    Create,
    Update,
    Delete,
    Transition,
}

impl AuditAction {
    pub fn as_str(&self) -> &'static str {
        match self {
            AuditAction::Create => "create",
            AuditAction::Update => "update",
            AuditAction::Delete => "delete",
            AuditAction::Transition => "transition",
        }
    }
}

/// One audit-trail row — who changed what, on which record, when, and why. Built by `CrudService`
/// after a write's own transaction has already committed (see `AuditTrailStore`'s doc comment for
/// why this can't be inside that transaction) and handed to whichever `AuditTrailStore` a
/// deployment has configured.
///
/// **`create`**: there is no real "before" state — `diff` is built against an empty `JsonObject`,
/// so every field reads as `{"before": null, "after": value}`. **`delete`**: `RecordDto` has no
/// `deleted` column at all (never selected by `metap-crud`'s own `RECORD_COLUMNS`), so a data diff
/// would show nothing and look like a false no-op — `delete` entries are action-based, not
/// diff-based: `diff` is left empty, the row's meaning carries entirely via `action`. Never
/// fabricate a synthetic `"deleted"` key here — that would misrepresent real field data as having
/// changed when it didn't.
pub struct AuditEntry {
    pub tenant_id: Uuid,
    pub entity: String,
    pub record_id: Uuid,
    pub action: AuditAction,
    /// The workflow action name (e.g. `"approve"`) — `Some` only when `action` is `Transition`.
    pub transition_action: Option<String>,
    /// Parsed from `RequestContext::user_id`, same as `CrudService`'s own `helpers::parse_user_id`.
    pub actor_user_id: Option<Uuid>,
    pub reason: Option<String>,
    /// `{field: {"before": ..., "after": ...}}`, changed keys only — see `crate::diff`.
    pub diff: JsonObject,
    pub version_after: Option<i32>,
    pub occurred_at: DateTime<Utc>,
}

/// One row read back from `metadata.audit_trail_entries` — the read side of
/// `AuditTrailStore::record`'s write. Kept as its own type rather than reusing `AuditEntry`
/// itself: a write never needs the row's own `id` (Postgres assigns it via `DEFAULT
/// gen_random_uuid()`, see `postgres_store.rs`'s `INSERT`), so a caller building an `AuditEntry`
/// to write would have nothing to put there — same split `metap-workflow` already draws between
/// its own write path (plain function args) and `WorkflowEvent` (its dedicated read-side row
/// type). `action`/`diff` stay the same wire shape `PostgresAuditTrailStore::record` wrote
/// (`action` as its lowercase string, `diff` as a raw JSON object) rather than round-tripping
/// through `AuditAction`, since nothing here needs to branch on the action as a Rust enum.
#[derive(Debug, Clone, sqlx::FromRow, serde::Serialize, utoipa::ToSchema)]
#[serde(rename_all = "camelCase")]
pub struct AuditTrailEntryRow {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub entity: String,
    pub record_id: Uuid,
    pub action: String,
    pub transition_action: Option<String>,
    pub actor_user_id: Option<Uuid>,
    pub reason: Option<String>,
    #[schema(value_type = Object)]
    pub diff: Value,
    pub version_after: Option<i32>,
    pub occurred_at: DateTime<Utc>,
}

/// Key of the marker that stands in for a redacted field's `{"before", "after"}` pair.
pub const REDACTED_MARKER: &str = "redacted";

/// Replaces every `fields` entry **already present** in `diff` with `{"redacted": true}`,
/// dropping the values while keeping the fact that the field changed.
///
/// Only rewrites keys the diff already has, never adds one. A field the write did not touch must
/// stay absent — the same rule `AuditEntry`'s own doc comment states for `delete` ("never
/// fabricate a synthetic key here"), and for the same reason: an audit trail that invents changes
/// is worse than one that omits values. It also means the marker carries real information — this
/// field changed at this point — rather than appearing on every entry regardless.
///
/// Applied by `metap-crud`'s `record_audit`, the single choke point all four writes pass through,
/// so no write path can persist a redacted field's value by forgetting to call this.
pub fn redact_diff_fields(diff: &mut JsonObject, fields: &[String]) {
    for field in fields {
        if let Some(slot) = diff.get_mut(field.as_str()) {
            *slot = Value::Object(Map::from_iter([(REDACTED_MARKER.to_string(), Value::Bool(true))]));
        }
    }
}

#[cfg(test)]
mod redaction_tests {
    use super::*;
    use serde_json::json;

    fn diff() -> JsonObject {
        json!({
            "amount": {"before": 4242, "after": 9999},
            "name": {"before": "old", "after": "new"},
        })
        .as_object()
        .unwrap()
        .clone()
    }

    #[test]
    fn a_redacted_field_keeps_the_fact_it_changed_but_loses_both_values() {
        let mut d = diff();
        redact_diff_fields(&mut d, &["amount".to_string()]);
        assert_eq!(d["amount"], json!({ REDACTED_MARKER: true }));
        // The plaintext must be gone from the entry entirely, not merely relabelled.
        assert!(!serde_json::to_string(&d).unwrap().contains("4242"));
        // ...and an unlisted field is untouched.
        assert_eq!(d["name"], json!({"before": "old", "after": "new"}));
    }

    /// A field the write never touched stays absent — same rule `AuditEntry`'s doc comment sets
    /// for `delete`. Marking it would invent a change that did not happen, and would also make
    /// the marker meaningless by putting it on every entry regardless.
    #[test]
    fn a_field_absent_from_the_diff_is_not_invented() {
        let mut d = diff();
        redact_diff_fields(&mut d, &["resolution".to_string()]);
        assert!(!d.contains_key("resolution"));
        assert_eq!(d.len(), 2);
    }

    #[test]
    fn redacting_nothing_leaves_the_diff_alone() {
        let mut d = diff();
        redact_diff_fields(&mut d, &[]);
        assert_eq!(d, diff());
    }
}
