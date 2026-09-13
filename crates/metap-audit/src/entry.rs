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
