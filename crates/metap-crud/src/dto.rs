use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

pub type JsonObject = serde_json::Map<String, serde_json::Value>;

/// `Deserialize` (alongside `Serialize`) exists specifically so `metap-grpc`'s `GrpcBackend` can
/// reconstruct a `RecordDto` from the JSON a remote `RecordService` RPC returns over the wire —
/// every in-process caller only ever constructs one directly and only needs `Serialize`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RecordDto {
    pub id: Uuid,
    pub entity: String,
    pub code: Option<String>,
    pub status: Option<String>,
    pub data: JsonObject,
    pub version: i32,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    /// List-only batch display hydration (`docs/roadmap.md`'s "Mode 2" —
    /// `CrudService::list`'s `hydrate_related_display`): keyed by the name of each
    /// `Reference` field on this entity that declares a `refDisplayField`, valued by that
    /// related record's display value — lets a list response show e.g. an assignee's name
    /// without the caller making one request per row per relation. `None` for every other
    /// response (`get`/`create`/`update`/`transition`/`delete`) and for a `list` response on
    /// an entity with no such field — this is additive, never required reading. A key is
    /// simply absent (not `null`) when the reference is dangling/unresolvable, or when the
    /// field itself didn't survive field-level read masking.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub related_display: Option<HashMap<String, String>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TransitionAvailability {
    pub action: String,
    pub available: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RecordCapabilities {
    pub writable_fields: Vec<String>,
    pub can_update: bool,
    /// Separate from `can_update` for the same reason `transitions` is: `Delete` is its own
    /// `EntityAction`, so a policy can grant editing without granting removal. Added 2026-09-03 —
    /// before it, a caller had no way to know whether a delete would be refused, so a UI either
    /// hid the button from people allowed to use it or offered it and let them hit a 403
    /// (`platform-ui/docs/audits/02-auth-permission-workflow-diagram-audit.md` finding B2).
    ///
    /// `#[serde(default)]` is about the **deserializing** direction only — `compute_capabilities`
    /// always sets this. `metap_grpc::client` parses a remote service's capabilities straight into
    /// this struct (`deserialize_capabilities`), so without a default, a gateway pointed at an
    /// upstream built before this field existed would fail the whole `get` on a missing key.
    /// Defaulting to `false` degrades that to a delete button hidden against an old upstream,
    /// which the server would have enforced regardless — much cheaper than losing the record.
    #[serde(default)]
    pub can_delete: bool,
    pub transitions: Vec<TransitionAvailability>,
}
