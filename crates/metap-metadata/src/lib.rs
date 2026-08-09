pub mod compiler;
pub mod entity;
pub mod openapi;
pub mod registry;

pub use compiler::{hash, validate, MetadataValidationError};
pub use entity::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, WorkflowTransition,
};
pub use openapi::generate_openapi_document;
pub use registry::{EntitySummary, MetadataRegistry, RegistryError};
