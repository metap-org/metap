//! Validates directly from `EntityField[]` (kind/required/enumValues) rather than a
//! per-entity, hand-authored schema — not just simpler, an improvement, since there's no
//! second schema to drift from `fields`, and it's exactly
//! the shape Phase 11's DB-authored metadata will need regardless (no per-entity source
//! file to hand-write a schema in once entities aren't code-authored).
//!
//! Known simplification vs. the Zod schemas seen in practice (e.g.
//! `apps/crm/src/modules/crm/customer.entity.ts`'s `email: z.string().email()`,
//! `referredBy: z.string().uuid()`): this validates JSON *type* per `FieldKind`
//! (string/number/boolean/enum-membership) and, since `EntityField.min`/`max`/`minLength`/
//! `maxLength` were added, numeric range and string-length bounds — but not free-form string
//! *formats* (email, UUID shape). A real format constraint would need a new metadata property
//! (e.g. a `pattern` regex), not a smarter validator; out of scope for this port.

use std::collections::HashMap;

use metap_metadata::{EntityDefinition, FieldKind};
use serde_json::Value;
use uuid::Uuid;

use crate::dto::JsonObject;

pub type FieldErrors = HashMap<String, Vec<String>>;

fn kind_matches(kind: FieldKind, value: &Value) -> bool {
    match kind {
        FieldKind::Id | FieldKind::String => value.is_string(),
        // A `Reference` field's value must be a syntactically valid UUID — not just any
        // string (found in code review, 2026-08-22: the old `is_string()`-only check let a
        // non-UUID, or a non-canonically-rendered UUID, e.g. uppercase hex, through
        // unvalidated. `CrudService::delete()`'s reference-integrity guard compares this
        // value against `id.to_string()` — Rust's `Uuid::to_string()` always renders
        // lowercase-hyphenated — with plain SQL string equality, so a non-canonical stored
        // value would silently fail to match a real reference, letting a delete slip through
        // that should have been rejected).
        FieldKind::Reference => value.as_str().is_some_and(|s| Uuid::parse_str(s).is_ok()),
        FieldKind::Number | FieldKind::Money => value.is_number(),
        FieldKind::Boolean => value.is_boolean(),
        FieldKind::Date | FieldKind::Datetime => value.is_string(),
        FieldKind::Json => true,
        FieldKind::Enum => false, // handled separately below (needs enum_values, not just kind)
    }
}

/// Validates `data` against `entity.fields`. Returns it unchanged on success for every kind
/// except `Reference` — this validator otherwise doesn't transform/default values the way
/// Zod's `.default()` can (see the module doc comment) — where the stored value is
/// canonicalized to `Uuid::to_string()`'s lowercase-hyphenated form regardless of how the
/// caller rendered it, so every later exact-string comparison against a `Reference` field
/// (`CrudService::delete()`'s reference-integrity guard, `hydrate_related_display`'s id
/// lookups) can rely on a single canonical form having been stored. On failure, returns
/// per-field error messages in the same `Record<string, string[]>` shape
/// `parsed.error.flatten().fieldErrors` produced.
pub fn validate_payload(entity: &EntityDefinition, data: &JsonObject) -> Result<JsonObject, FieldErrors> {
    let mut errors: FieldErrors = FieldErrors::new();
    let mut result = data.clone();

    for field in &entity.fields {
        let value = data.get(&field.name).filter(|v| !v.is_null());

        if field.required.unwrap_or(false) && value.is_none() {
            errors
                .entry(field.name.clone())
                .or_default()
                .push("required".to_string());
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
            errors
                .entry(field.name.clone())
                .or_default()
                .push("invalid_type".to_string());
            continue;
        }

        if field.kind == FieldKind::Reference {
            // `valid` above already confirmed this parses.
            let canonical = Uuid::parse_str(value.as_str().expect("checked by kind_matches")).unwrap();
            result.insert(field.name.clone(), Value::String(canonical.to_string()));
        }

        // `compiler::validate` already rejects `min`/`max`/`minLength`/`maxLength` set on a
        // kind they don't apply to, so it's safe to check both pairs unconditionally here —
        // only the pair matching this field's actual kind (Number/Money vs. String) will ever
        // be `Some`.
        if let (FieldKind::Number | FieldKind::Money, Some(n)) = (field.kind, value.as_f64()) {
            if let Some(min) = field.min {
                if n < min {
                    errors.entry(field.name.clone()).or_default().push("min".to_string());
                }
            }
            if let Some(max) = field.max {
                if n > max {
                    errors.entry(field.name.clone()).or_default().push("max".to_string());
                }
            }
        }
        if let (FieldKind::String, Some(s)) = (field.kind, value.as_str()) {
            let len = s.chars().count() as u32;
            if let Some(min_length) = field.min_length {
                if len < min_length {
                    errors
                        .entry(field.name.clone())
                        .or_default()
                        .push("min_length".to_string());
                }
            }
            if let Some(max_length) = field.max_length {
                if len > max_length {
                    errors
                        .entry(field.name.clone())
                        .or_default()
                        .push("max_length".to_string());
                }
            }
        }
    }

    if errors.is_empty() {
        Ok(result)
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
            storage: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
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

    #[test]
    fn reference_field_with_a_non_uuid_string_is_rejected() {
        let e = entity(vec![field("parentId", FieldKind::Reference, false)]);
        let data = obj(&[("parentId", json!("not-a-uuid"))]);
        let errors = validate_payload(&e, &data).unwrap_err();
        assert_eq!(errors["parentId"], vec!["invalid_type".to_string()]);
    }

    #[test]
    fn reference_field_value_is_canonicalized_to_lowercase() {
        let e = entity(vec![field("parentId", FieldKind::Reference, false)]);
        // Same UUID as `Uuid::new_v4().to_string()` would render, but uppercase — a client
        // using a different UUID library, or hand-crafting a request, could send this.
        let data = obj(&[("parentId", json!("550E8400-E29B-41D4-A716-446655440000"))]);
        let result = validate_payload(&e, &data).unwrap();
        assert_eq!(result["parentId"], json!("550e8400-e29b-41d4-a716-446655440000"));
    }

    #[test]
    fn number_below_min_is_rejected() {
        let mut f = field("qty", FieldKind::Number, false);
        f.min = Some(1.0);
        let e = entity(vec![f]);
        let errors = validate_payload(&e, &obj(&[("qty", json!(0))])).unwrap_err();
        assert_eq!(errors["qty"], vec!["min".to_string()]);
    }

    #[test]
    fn number_above_max_is_rejected() {
        let mut f = field("qty", FieldKind::Number, false);
        f.max = Some(10.0);
        let e = entity(vec![f]);
        let errors = validate_payload(&e, &obj(&[("qty", json!(11))])).unwrap_err();
        assert_eq!(errors["qty"], vec!["max".to_string()]);
    }

    #[test]
    fn number_within_min_max_passes() {
        let mut f = field("qty", FieldKind::Number, false);
        f.min = Some(1.0);
        f.max = Some(10.0);
        let e = entity(vec![f]);
        assert!(validate_payload(&e, &obj(&[("qty", json!(5))])).is_ok());
    }

    #[test]
    fn string_shorter_than_min_length_is_rejected() {
        let mut f = field("code", FieldKind::String, false);
        f.min_length = Some(3);
        let e = entity(vec![f]);
        let errors = validate_payload(&e, &obj(&[("code", json!("ab"))])).unwrap_err();
        assert_eq!(errors["code"], vec!["min_length".to_string()]);
    }

    #[test]
    fn string_longer_than_max_length_is_rejected() {
        let mut f = field("code", FieldKind::String, false);
        f.max_length = Some(3);
        let e = entity(vec![f]);
        let errors = validate_payload(&e, &obj(&[("code", json!("abcd"))])).unwrap_err();
        assert_eq!(errors["code"], vec!["max_length".to_string()]);
    }

    #[test]
    fn string_within_length_bounds_passes() {
        let mut f = field("code", FieldKind::String, false);
        f.min_length = Some(2);
        f.max_length = Some(4);
        let e = entity(vec![f]);
        assert!(validate_payload(&e, &obj(&[("code", json!("abc"))])).is_ok());
    }
}
