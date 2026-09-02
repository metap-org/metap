pub mod compiler;
pub mod entity;
pub mod openapi;
pub mod registry;

pub use compiler::{hash, validate, MetadataValidationError};
pub use entity::{
    field_has_real_column, field_kind_sql_type, resolve_field_storage_tier, EntityDefinition, EntityField,
    EntityListView, EntityWorkflow, FieldDisplayHint, FieldKind, FieldStorage, FieldStorageTier, RelatedView,
    WorkflowTransition,
};
pub use openapi::generate_openapi_document;
pub use registry::{EntitySummary, MetadataRegistry, RegistryError};
