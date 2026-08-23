//! JSON field names (camelCase) and `FieldKind`'s wire values are what a served
//! `/metadata/openapi.json` and `/metadata/entities` response need to produce for
//! `packages/platform-react`'s `openapi-typescript` codegen to keep working.
//!
//! Deliberately has no `schema` field — request-payload validation is a validator
//! generated directly from `fields` (kind/required/enumValues, see
//! `crates/metap-crud/src/validation.rs`) rather than a hand-authored, separately-maintained
//! schema. That validator is `CrudService`-layer work, not part of the metadata shape
//! itself.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum FieldKind {
    Id,
    String,
    Number,
    Boolean,
    Date,
    Datetime,
    Money,
    Enum,
    Reference,
    Json,
}

/// Explicit override of the storage tier `resolve_field_storage_tier` would otherwise derive
/// from `indexed`/`sortable`/`unique`/`searchable` (`docs/multi-tenant-platform-design.md` §3.2,
/// `docs/features/04-table-per-entity.md` step 1). Metadata only today — the shared `records`
/// JSONB table (`crates/metap-crud`) does not yet consult this; it exists so a future
/// per-entity reconciler has "where should this field live" as input without re-deriving the
/// rule. `None` (the common case) means "derive from flags".
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum FieldStorage {
    /// Force T1 (stay inside `data jsonb`) even if indexed/sortable/unique/searchable is set.
    Native,
    /// Force promotion to a generated column even without any of those flags set.
    Column,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityField {
    pub name: String,
    pub label: String,
    pub kind: FieldKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub required: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indexed: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unique: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub enum_values: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ref_entity: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ref_display_field: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub searchable: Option<bool>,
    /// "substring" (default) or "fts" — only meaningful when `searchable: true`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub search_mode: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sortable: Option<bool>,
    /// See `FieldStorage`'s doc comment. `None` (default) derives the tier from the flags above.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub storage: Option<FieldStorage>,
}

/// The storage tier a field is promoted to under table-per-entity
/// (`docs/multi-tenant-platform-design.md` §3.2). Purely advisory today, consumed by nothing at
/// runtime — the shared `records` table stores every field in `data jsonb` regardless of tier.
/// It is the input a future per-entity reconciler needs to decide "generate a column for this
/// field or leave it in JSONB", without re-deriving the promotion rule at that point.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FieldStorageTier {
    /// T1 — stays inside `data jsonb`, no promotion.
    Jsonb,
    /// T2 — promoted to a generated column (or trigram/tsvector GIN index for `searchable`),
    /// computed from `data jsonb`. T3 (a fully real, non-generated column) is an entity-level
    /// decision for very hot entities, not derived per-field here.
    GeneratedColumn,
}

/// Derives a field's storage tier from its `storage` override, falling back to
/// `indexed`/`sortable`/`unique`/`searchable` when unset. See `FieldStorage`'s doc comment.
pub fn resolve_field_storage_tier(field: &EntityField) -> FieldStorageTier {
    match field.storage {
        Some(FieldStorage::Native) => FieldStorageTier::Jsonb,
        Some(FieldStorage::Column) => FieldStorageTier::GeneratedColumn,
        None => {
            if field.indexed == Some(true)
                || field.sortable == Some(true)
                || field.unique == Some(true)
                || field.searchable == Some(true)
            {
                FieldStorageTier::GeneratedColumn
            } else {
                FieldStorageTier::Jsonb
            }
        }
    }
}

/// Whether `field` gets a real physical column under table-per-entity, independent of
/// `resolve_field_storage_tier` (most promoted fields stay in `data jsonb`, just with an
/// expression index — see `crates/metap-reconciler/src/compile.rs`'s doc comment). Only two
/// cases force a real column: a `Reference` with `ref_entity` set (a FK constraint has to attach
/// to a real column, not a JSONB path) or an explicit `storage: Some(FieldStorage::Column)`
/// override. Single source of truth for both `metap-reconciler::compile` (decides DDL) and
/// `metap-query` (builds SQL against it) so the two can never disagree about which fields have a
/// real column.
pub fn field_has_real_column(field: &EntityField) -> bool {
    (field.kind == FieldKind::Reference && field.ref_entity.is_some()) || field.storage == Some(FieldStorage::Column)
}

/// `FieldKind` → SQL type a promoted (`FieldStorageTier::GeneratedColumn`) field would use
/// (`docs/multi-tenant-platform-design.md` §3.2's mapping table). `Money` maps to
/// `numeric(18,4)`, never a float type, to avoid rounding error on currency amounts;
/// `Reference`/`Id` map to `uuid` so a future table-per-entity FK can attach to the column
/// directly. Not consumed by any reconciler yet — see `FieldStorageTier`'s doc comment.
pub fn field_kind_sql_type(kind: FieldKind) -> &'static str {
    match kind {
        FieldKind::String | FieldKind::Enum => "text",
        FieldKind::Number => "double precision",
        FieldKind::Money => "numeric(18,4)",
        FieldKind::Boolean => "boolean",
        FieldKind::Date => "date",
        FieldKind::Datetime => "timestamptz",
        FieldKind::Reference | FieldKind::Id => "uuid",
        FieldKind::Json => "jsonb",
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityListView {
    pub name: String,
    pub label: String,
    pub fields: Vec<String>,
    pub filters: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub default_sort: Option<String>,
    pub max_limit: u32,
}

/// `guard` is a `PolicyCondition` (`metap-permission`, the same declarative type policies
/// use) rather than a server-side predicate function — this is why this crate depends on
/// `metap-permission`. It crosses the wire like any other field (`GET /metadata/entities` is
/// authenticated, not admin-gated, so any tenant user can already see it — same exposure
/// level as the `capabilities.transitions` guard *results* `CrudService::get` already
/// computes from it). Unlike the old TS-era shape (`entity-wire-schema.ts`, where `guard` was
/// a server-side predicate *function* and genuinely couldn't be serialized), the Rust port's
/// guard was always data — it stayed `#[serde(skip)]` here only to mirror that old exclusion,
/// not because anything about `PolicyCondition` prevents it. Un-skipped 2026-08-17 (Phase 11
/// Phase B, `docs/roadmap.md`) so DB-authored (low-code) entities can carry a workflow with
/// guards at all — `metap-lowcode`'s `LowCodeEntityDefinition` round-trips this same struct
/// through Postgres `jsonb`, and `#[serde(skip)]` would have silently dropped every guard on
/// every save. `metap_workflow::run_guard` was already entity-agnostic before this change
/// (it evaluates a `PolicyCondition` against record data + context, no code-authored
/// assumption anywhere) — this was purely a serialization gap, not a runtime one.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkflowTransition {
    pub action: String,
    pub from: String,
    pub to: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub guard: Option<metap_permission::PolicyCondition>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityWorkflow {
    pub state_field: String,
    pub initial_state: String,
    pub terminal_states: Vec<String>,
    pub transitions: Vec<WorkflowTransition>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityDefinition {
    pub name: String,
    pub label: String,
    pub table_name: String,
    pub fields: Vec<EntityField>,
    pub list_views: Vec<EntityListView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub workflow: Option<EntityWorkflow>,
}

#[cfg(test)]
mod storage_tier_tests {
    use super::*;

    fn field(kind: FieldKind) -> EntityField {
        EntityField {
            name: "f".to_string(),
            label: "F".to_string(),
            kind,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }
    }

    #[test]
    fn no_flags_and_no_override_stays_jsonb() {
        assert_eq!(
            resolve_field_storage_tier(&field(FieldKind::String)),
            FieldStorageTier::Jsonb
        );
    }

    #[test]
    fn indexed_promotes_to_generated_column() {
        let mut f = field(FieldKind::String);
        f.indexed = Some(true);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::GeneratedColumn);
    }

    #[test]
    fn sortable_promotes_to_generated_column() {
        let mut f = field(FieldKind::Datetime);
        f.sortable = Some(true);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::GeneratedColumn);
    }

    #[test]
    fn unique_promotes_to_generated_column() {
        let mut f = field(FieldKind::String);
        f.unique = Some(true);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::GeneratedColumn);
    }

    #[test]
    fn searchable_promotes_to_generated_column() {
        let mut f = field(FieldKind::String);
        f.searchable = Some(true);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::GeneratedColumn);
    }

    #[test]
    fn native_override_forces_jsonb_even_when_indexed() {
        let mut f = field(FieldKind::String);
        f.indexed = Some(true);
        f.storage = Some(FieldStorage::Native);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::Jsonb);
    }

    #[test]
    fn column_override_forces_promotion_with_no_flags_set() {
        let mut f = field(FieldKind::String);
        f.storage = Some(FieldStorage::Column);
        assert_eq!(resolve_field_storage_tier(&f), FieldStorageTier::GeneratedColumn);
    }

    #[test]
    fn field_kind_sql_type_mapping_matches_the_design_doc_table() {
        assert_eq!(field_kind_sql_type(FieldKind::String), "text");
        assert_eq!(field_kind_sql_type(FieldKind::Enum), "text");
        assert_eq!(field_kind_sql_type(FieldKind::Number), "double precision");
        assert_eq!(field_kind_sql_type(FieldKind::Money), "numeric(18,4)");
        assert_eq!(field_kind_sql_type(FieldKind::Boolean), "boolean");
        assert_eq!(field_kind_sql_type(FieldKind::Date), "date");
        assert_eq!(field_kind_sql_type(FieldKind::Datetime), "timestamptz");
        assert_eq!(field_kind_sql_type(FieldKind::Reference), "uuid");
        assert_eq!(field_kind_sql_type(FieldKind::Id), "uuid");
        assert_eq!(field_kind_sql_type(FieldKind::Json), "jsonb");
    }

    #[test]
    fn storage_field_round_trips_through_json_camel_case() {
        let mut f = field(FieldKind::String);
        f.storage = Some(FieldStorage::Column);
        let json = serde_json::to_value(&f).unwrap();
        assert_eq!(json["storage"], serde_json::json!("column"));
        let back: EntityField = serde_json::from_value(json).unwrap();
        assert_eq!(back.storage, Some(FieldStorage::Column));
    }

    #[test]
    fn storage_omitted_from_json_when_none() {
        let json = serde_json::to_value(field(FieldKind::String)).unwrap();
        assert!(json.get("storage").is_none());
    }

    #[test]
    fn missing_storage_key_deserializes_to_none_for_old_low_code_definitions() {
        let json = serde_json::json!({
            "name": "f", "label": "F", "kind": "string",
        });
        let f: EntityField = serde_json::from_value(json).unwrap();
        assert_eq!(f.storage, None);
    }
}
