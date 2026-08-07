//! Where the TS `CrudService` calls `entity.schema.safeParse(rawData)` — a per-entity,
//! hand-authored Zod schema kept in sync with `entity.fields` by hand — this validates
//! directly from `EntityField[]` (kind/required/enumValues). Per
//! `docs/rust-core-viability.md`'s Schema & Codegen Strategy: not just parity, an
//! improvement, since there's no second schema to drift from `fields`, and it's exactly
//! the shape Phase 11's DB-authored metadata will need regardless (no per-entity source
//! file to hand-write a schema in once entities aren't code-authored).
//!
//! Known simplification vs. the Zod schemas seen in practice (e.g.
//! `apps/crm/src/modules/crm/customer.entity.ts`'s `email: z.string().email()`,
//! `referredBy: z.string().uuid()`): this validates JSON *type* per `FieldKind`
//! (string/number/boolean/enum-membership), not per-field string formats (email, UUID
//! shape, length bounds) — `EntityField` metadata has no format/length concept to drive
//! that from. A real format constraint would need a new metadata property, not a smarter
//! validator; out of scope for this port.

use std::collections::HashMap;

use metap_metadata::{EntityDefinition, FieldKind};
use serde_json::Value;

use crate::dto::JsonObject;

pub type FieldErrors = HashMap<String, Vec<String>>;

fn kind_matches(kind: FieldKind, value: &Value) -> bool {
    match kind {
        FieldKind::Id | FieldKind::String | FieldKind::Reference => value.is_string(),
        FieldKind::Number | FieldKind::Money => value.is_number(),
        FieldKind::Boolean => value.is_boolean(),
        FieldKind::Date | FieldKind::Datetime => value.is_string(),
        FieldKind::Json => true,
        FieldKind::Enum => false, // handled separately below (needs enum_values, not just kind)
    }
}

/// Validates `data` against `entity.fields` and returns it unchanged on success (this
/// validator doesn't transform/default values the way Zod's `.default()` can — see the
/// module doc comment). On failure, returns per-field error messages in the same
/// `Record<string, string[]>` shape `parsed.error.flatten().fieldErrors` produced.
pub fn validate_payload(
    entity: &EntityDefinition,
    data: &JsonObject,
) -> Result<JsonObject, FieldErrors> {
    let mut errors: FieldErrors = FieldErrors::new();

    for field in &entity.fields {
        let value = data.get(&field.name).filter(|v| !v.is_null());

        if field.required.unwrap_or(false) && value.is_none() {
            errors.entry(field.name.clone()).or_default().push("required".to_string());
            continue;
        }

        let Some(value) = value else { continue };

        let valid = if matches!(field.kind, FieldKind::Enum) {
            value
                .as_str()
                .is_some_and(|s| field.enum_values.as_ref().is_some_and(|vs| vs.iter().any(|v| v == s)))
        } else {
            kind_matches(field.kind, value)
        };

        if !valid {
            errors.entry(field.name.clone()).or_default().push("invalid_type".to_string());
        }
    }

    if errors.is_empty() {
        Ok(data.clone())
    } else {
        Err(errors)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use metap_metadata::EntityField;
    use serde_json::json;

    fn field(name: &str, kind: FieldKind, required: bool) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
            required: required.then_some(true),
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
        }
    }

    fn entity(fields: Vec<EntityField>) -> EntityDefinition {
        EntityDefinition {
            name: "test.widgets".to_string(),
            label: "Widget".to_string(),
            table_name: "records".to_string(),
            fields,
            list_views: vec![],
            workflow: None,
        }
    }

    fn obj(pairs: &[(&str, Value)]) -> JsonObject {
        pairs.iter().map(|(k, v)| (k.to_string(), v.clone())).collect()
    }

    #[test]
    fn missing_required_field_is_an_error() {
        let e = entity(vec![field("name", FieldKind::String, true)]);
        let errors = validate_payload(&e, &JsonObject::new()).unwrap_err();
        assert_eq!(errors["name"], vec!["required".to_string()]);
    }

    #[test]
    fn present_required_field_passes() {
        let e = entity(vec![field("name", FieldKind::String, true)]);
        let data = obj(&[("name", json!("Acme"))]);
        assert!(validate_payload(&e, &data).is_ok());
    }

    #[test]
    fn wrong_json_type_is_rejected() {
        let e = entity(vec![field("score", FieldKind::Number, false)]);
        let data = obj(&[("score", json!("not-a-number"))]);
        let errors = validate_payload(&e, &data).unwrap_err();
        assert_eq!(errors["score"], vec!["invalid_type".to_string()]);
    }

    #[test]
    fn enum_value_not_in_enum_values_is_rejected() {
        let mut f = field("status", FieldKind::Enum, false);
        f.enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
        let e = entity(vec![f]);
        let data = obj(&[("status", json!("bogus"))]);
        assert!(validate_payload(&e, &data).is_err());
    }

    #[test]
    fn enum_value_in_enum_values_passes() {
        let mut f = field("status", FieldKind::Enum, false);
        f.enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
        let e = entity(vec![f]);
        let data = obj(&[("status", json!("active"))]);
        assert!(validate_payload(&e, &data).is_ok());
    }

    #[test]
    fn unknown_extra_fields_pass_through_unvalidated() {
        let e = entity(vec![field("name", FieldKind::String, true)]);
        let data = obj(&[("name", json!("Acme")), ("mystery", json!(123))]);
        let result = validate_payload(&e, &data).unwrap();
        assert_eq!(result["mystery"], json!(123));
    }

    #[test]
    fn optional_field_absent_is_fine() {
        let e = entity(vec![field("phone", FieldKind::String, false)]);
        assert!(validate_payload(&e, &JsonObject::new()).is_ok());
    }
}
