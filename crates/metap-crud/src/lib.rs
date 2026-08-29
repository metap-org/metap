pub mod crud_service;
pub mod dto;
pub mod record_backend;
pub mod result;
pub mod validation;

pub use crud_service::CrudService;
pub use dto::{JsonObject, RecordCapabilities, RecordDto, TransitionAvailability};
pub use record_backend::RecordBackend;
pub use result::{PageInfo, ServiceResult};
pub use validation::{validate_payload, FieldErrors};
