//! `RecordHandle` is what every entity `Object` type's field resolvers read from
//! (`ctx.parent_value.downcast_ref::<RecordHandle>()`) — it holds the *already-serialized* JSON
//! form of a `RecordDto` (`{id, entity, code, status, data: {...}, version, createdAt,
//! updatedAt}`, exactly `serde_json::to_value(dto)`'s shape) rather than the typed `RecordDto`
//! itself, so every scalar field resolver is the same one-line JSON-pointer lookup regardless of
//! whether the field lives at the top level (`id`, `version`, ...) or inside `data` (every
//! `EntityField`). This is also *why* field-level permission masking needs no extra code here at
//! all: `CrudService`'s `get`/`get_many`/`list`/etc. already ran `filter_readable_fields` before
//! returning a `RecordDto`, so a denied field's key is simply absent from `data` — a JSON-pointer
//! lookup for an absent key returns `None`, which every resolver already treats as GraphQL
//! `null`. There is no second masking pass to write or forget.

use metap_crud::{RecordCapabilities, RecordDto};
use serde_json::Value;

#[derive(Clone)]
pub struct RecordHandle {
    json: Value,
}

impl RecordHandle {
    pub fn from_dto(dto: RecordDto) -> Self {
        Self {
            json: serde_json::to_value(dto).unwrap_or(Value::Null),
        }
    }

    /// `get`'s response additionally carries `RecordCapabilities` — merged into the same JSON
    /// object under a `capabilities` key, exactly mirroring how
    /// `crates/metap-http/src/routes/records.rs`'s REST `get_record` handler merges the two for
    /// its JSON response, so both transports expose capabilities the same way.
    pub fn from_dto_with_capabilities(dto: RecordDto, capabilities: RecordCapabilities) -> Self {
        let mut json = serde_json::to_value(dto).unwrap_or(Value::Null);
        if let Value::Object(map) = &mut json {
            map.insert(
                "capabilities".to_string(),
                serde_json::to_value(capabilities).unwrap_or(Value::Null),
            );
        }
        Self { json }
    }

    /// A top-level `RecordDto` field: `id`/`entity`/`code`/`status`/`version`/`createdAt`/
    /// `updatedAt`/`capabilities`.
    pub fn top_level(&self, key: &str) -> Option<&Value> {
        self.json.get(key)
    }

    /// A business field — always nested under `data` (see this module's doc comment).
    pub fn data_field(&self, field_name: &str) -> Option<&Value> {
        self.json.get("data").and_then(|d| d.get(field_name))
    }
}
