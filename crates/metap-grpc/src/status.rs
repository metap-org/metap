//! Maps a `ServiceResult::Err` onto a `tonic::Status` and back — the gRPC counterpart to
//! `crates/metap-http/src/error.rs`'s `service_error_response`, so the same `CrudService` failure
//! reads the same regardless of which transport a caller used.
//!
//! **The `Code` alone is not the contract; [`ErrorDetails`] is** (audit 04 finding B#2, fixed
//! 2026-09-03). A `tonic::Status` carries only a `Code` plus free text, so this mapping used to
//! throw away everything a caller actually needs to act on a failure: the exact numeric HTTP
//! status (`400` and `422` both collapse into `InvalidArgument`, and `500`/`503` both into
//! `Internal`), the stable `error` code string, and — worst — `field_errors`, which was reduced to
//! a **count** appended to the message (`"Validation failed (3 field error(s))"`). Through
//! `graphql-gateway` that was the whole story a client got: a form could be told it had three
//! problems but never which fields. The platform generates that per-field detail from entity
//! metadata for free; only REST was actually delivering it.
//!
//! So the full envelope now rides in `Status`'s `details` as JSON, and [`error_details_from_status`]
//! (used by `crate::client`) reconstructs the original `ServiceResult::Err` exactly. Details are a
//! standard, optional part of the gRPC status contract — a caller that ignores them, or an older
//! server that never sets them, still sees the same `Code` + message as before, so this is additive
//! in both directions. JSON rather than `google.rpc.BadRequest` deliberately: the envelope is
//! already the exact shape `ServiceResult::Err` and `metap-http`'s REST error body use, so one
//! representation covers all three transports without a second proto to keep in sync.

use std::collections::HashMap;

use metap_crud::ServiceResult;
use serde::{Deserialize, Serialize};
use tonic::{Code, Status};

/// The `ServiceResult::Err` payload, carried verbatim in a `Status`'s details.
///
/// Field names are camelCase to match the REST error body (`metap-http`'s
/// `service_error_response`) — a caller reading `fieldErrors` off a GraphQL error extension, a
/// REST response, or a decoded gRPC status sees the same key either way.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ErrorDetails {
    pub status: u16,
    pub error: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub field_errors: Option<HashMap<String, Vec<String>>>,
}

/// Numeric HTTP status → the semantically closest gRPC code. Kept keyed off the status (not the
/// `error` string) since that's the smaller, already-stable contract `ServiceResult` exposes — a
/// new `error` code string can be added to `metap-crud` without this mapping needing to change.
/// Lossy by nature, which is exactly why [`ErrorDetails`] carries the original alongside it.
fn code_for_status(status: u16) -> Code {
    match status {
        400 | 422 => Code::InvalidArgument,
        401 => Code::Unauthenticated,
        403 => Code::PermissionDenied,
        404 => Code::NotFound,
        409 => Code::Aborted, // optimistic-lock version conflicts land here (REST: 409)
        _ => Code::Internal,
    }
}

pub fn error_to_status(
    status: u16,
    error: String,
    message: Option<String>,
    field_errors: Option<HashMap<String, Vec<String>>>,
) -> Status {
    let code = code_for_status(status);
    let text = message.clone().unwrap_or_else(|| error.clone());
    let details = ErrorDetails {
        status,
        error,
        message,
        field_errors,
    };
    match serde_json::to_vec(&details) {
        Ok(bytes) => Status::with_details(code, text, bytes.into()),
        // Serializing a struct of plain String/HashMap fields cannot realistically fail; degrade to
        // a detail-free status rather than turning a business error into an internal one.
        Err(_) => Status::new(code, text),
    }
}

/// Reads the [`ErrorDetails`] back off a `Status`, if the peer set them. `None` for a status from
/// anything that doesn't speak this envelope (an older `metap` build, a proxy-generated
/// `UNAVAILABLE`, a mesh sidecar's own error) — callers fall back to `Code`-based reconstruction.
pub fn error_details_from_status(status: &Status) -> Option<ErrorDetails> {
    let details = status.details();
    if details.is_empty() {
        return None;
    }
    serde_json::from_slice(details).ok()
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

#[cfg(test)]
mod tests {
    use super::*;

    fn field_errors() -> HashMap<String, Vec<String>> {
        HashMap::from([
            ("name".to_string(), vec!["is required".to_string()]),
            ("email".to_string(), vec!["must be an email".to_string()]),
        ])
    }

    /// The regression audit 04 B#2 is about: a validation failure must survive the gRPC hop with
    /// its per-field detail intact, not as a count in a sentence.
    #[test]
    fn field_errors_round_trip_through_a_status() {
        let status = error_to_status(
            422,
            "validation_failed".to_string(),
            Some("Validation failed".to_string()),
            Some(field_errors()),
        );
        assert_eq!(status.code(), Code::InvalidArgument);

        let details = error_details_from_status(&status).expect("details must be set");
        assert_eq!(details.status, 422);
        assert_eq!(details.error, "validation_failed");
        assert_eq!(details.message.as_deref(), Some("Validation failed"));
        let recovered = details.field_errors.expect("field errors must survive");
        assert_eq!(recovered.get("name").unwrap(), &vec!["is required".to_string()]);
        assert_eq!(recovered.len(), 2);
    }

    /// `400` and `422` share one gRPC `Code`, so the numeric status is only recoverable from the
    /// details — this is what stops the client from having to guess it back.
    #[test]
    fn the_exact_numeric_status_survives_even_when_the_code_is_ambiguous() {
        for status_code in [400u16, 422] {
            let status = error_to_status(status_code, "bad".to_string(), None, None);
            assert_eq!(status.code(), Code::InvalidArgument);
            assert_eq!(error_details_from_status(&status).unwrap().status, status_code);
        }
    }

    #[test]
    fn the_message_still_reads_the_same_for_a_caller_that_ignores_details() {
        let with_message = error_to_status(404, "not_found".to_string(), Some("No such record".to_string()), None);
        assert_eq!(with_message.message(), "No such record");
        // No `message` → the stable `error` code is the human-facing text, unchanged from before.
        let without_message = error_to_status(404, "not_found".to_string(), None, None);
        assert_eq!(without_message.message(), "not_found");
    }

    #[test]
    fn a_status_with_no_details_is_reported_as_absent_not_as_an_error() {
        let bare = Status::new(Code::Unavailable, "upstream is down");
        assert!(error_details_from_status(&bare).is_none());
    }
}
