//! `FieldKind` -> GraphQL `TypeRef` — the dynamic-schema counterpart to
//! `metap-metadata/src/openapi.rs`'s `field_kind_json_schema`. Every field this produces a
//! `TypeRef` for is nullable (never `named_nn`) regardless of `EntityField.required` or the
//! underlying column's own nullability: a field masked out by `PermissionSnapshot::
//! filter_readable_fields` must degrade to GraphQL `null`, not a non-null-field violation error
//! — see `schema.rs`'s doc comment for how that masking already happens for free, purely because
//! resolvers read from an already-masked `RecordDto.data`.

use async_graphql::dynamic::TypeRef;
use metap_metadata::FieldKind;

/// Name of the custom scalar registered once per schema for anything that doesn't map to a
/// GraphQL builtin scalar — currently only entity fields of kind `Json`, plus the `filter`/
/// `data` input arguments every list/create/update/transition field takes (see `schema.rs`).
/// Round-trips through `async_graphql::Value::{from_json, into_json}` with no extra validation,
/// same as any JSON-shaped GraphQL scalar.
pub const JSON_SCALAR: &str = "Json";

/// `ref_entity_type_name` is only needed for `FieldKind::Reference` (the target entity's own
/// GraphQL object type name, via `naming::type_name`) — passed in rather than computed here so
/// this module stays free of any entity-registry lookup.
pub fn scalar_type_ref(kind: FieldKind, ref_entity_type_name: Option<&str>) -> TypeRef {
    match kind {
        FieldKind::Id => TypeRef::named(TypeRef::ID),
        FieldKind::String | FieldKind::Enum | FieldKind::Date | FieldKind::Datetime => TypeRef::named(TypeRef::STRING),
        FieldKind::Number | FieldKind::Money => TypeRef::named(TypeRef::FLOAT),
        FieldKind::Boolean => TypeRef::named(TypeRef::BOOLEAN),
        FieldKind::Json => TypeRef::named(JSON_SCALAR),
        FieldKind::Reference => {
            TypeRef::named(ref_entity_type_name.expect("Reference field must supply a target type name"))
        }
    }
}
