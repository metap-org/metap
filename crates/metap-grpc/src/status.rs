//! Maps a `ServiceResult::Err`'s numeric HTTP status to a `tonic::Status` code — the gRPC
//! counterpart to `crates/metap-http/src/error.rs`'s `service_error_response`, so the same
//! `CrudService` failure reads as the semantically closest gRPC status regardless of which
//! transport a caller used. Keyed off the numeric status (not the `error` string code) since
//! that's the smaller, already-stable contract `ServiceResult` exposes — a new `error` code
//! string can be added to `metap-crud` without this mapping needing to change.

use std::collections::HashMap;

use metap_crud::ServiceResult;
use tonic::{Code, Status};

pub fn error_to_status(
    status: u16,
    error: String,
    message: Option<String>,
    field_errors: Option<HashMap<String, Vec<String>>>,
) -> Status {
    let code = match status {
        400 | 422 => Code::InvalidArgument,
        401 => Code::Unauthenticated,
        403 => Code::PermissionDenied,
        404 => Code::NotFound,
        409 => Code::Aborted, // optimistic-lock version conflicts land here (REST: 409)
        _ => Code::Internal,
    };
    let text = message.unwrap_or(error);
    let field_errors_suffix = field_errors
        .map(|fe| format!(" ({} field error(s))", fe.len()))
        .unwrap_or_default();
    Status::new(code, format!("{text}{field_errors_suffix}"))
}

pub fn service_result_to_status<T>(result: ServiceResult<T>) -> Result<T, Status> {
    match result {
        ServiceResult::Ok { data, .. } => Ok(data),
        ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        } => Err(error_to_status(status, error, message, field_errors)),
    }
}

pub fn internal(err: anyhow::Error) -> Status {
    tracing::error!(error = %format!("{err:#}"), "internal error");
    Status::internal("internal server error")
}
