use chrono::{DateTime, Utc};
use serde::Serialize;
use uuid::Uuid;

pub type JsonObject = serde_json::Map<String, serde_json::Value>;

#[derive(Debug, Clone, Serialize)]
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
}

#[derive(Debug, Clone, Serialize)]
pub struct TransitionAvailability {
    pub action: String,
    pub available: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RecordCapabilities {
    pub writable_fields: Vec<String>,
    pub can_update: bool,
    pub transitions: Vec<TransitionAvailability>,
}
