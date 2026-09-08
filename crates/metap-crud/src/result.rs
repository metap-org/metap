//! Mirrors `packages/core/src/core/crud/result.ts`.

use std::collections::HashMap;

#[derive(Debug, Clone)]
pub struct PageInfo {
    pub limit: i64,
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone)]
pub enum ServiceResult<T> {
    Ok {
        data: T,
        page: Option<PageInfo>,
    },
    Err {
        status: u16,
        error: String,
        message: Option<String>,
        field_errors: Option<HashMap<String, Vec<String>>>,
    },
}

impl<T> ServiceResult<T> {
    pub fn ok(data: T) -> Self {
        ServiceResult::Ok { data, page: None }
    }

    pub fn ok_with_page(data: T, page: PageInfo) -> Self {
        ServiceResult::Ok { data, page: Some(page) }
    }

    pub fn err(status: u16, error: impl Into<String>) -> Self {
        ServiceResult::Err {
            status,
            error: error.into(),
            message: None,
            field_errors: None,
        }
    }

    pub fn err_with_message(status: u16, error: impl Into<String>, message: impl Into<String>) -> Self {
        ServiceResult::Err {
            status,
            error: error.into(),
            message: Some(message.into()),
            field_errors: None,
        }
    }

    pub fn err_with_field_errors(
        status: u16,
        error: impl Into<String>,
        field_errors: HashMap<String, Vec<String>>,
    ) -> Self {
        ServiceResult::Err {
            status,
            error: error.into(),
            message: None,
            field_errors: Some(field_errors),
        }
    }

    /// Both a human-readable `message` (for a client that just shows it as-is, same as
    /// `err_with_message`) and structured `field_errors` (for a client that wants to do more,
    /// same wire shape `err_with_field_errors` already produces) on the same error — used by
    /// `delete()`'s `record_referenced` guard, which needs a readable summary of the first
    /// blocker *and* the full list every blocker maps to.
    pub fn err_with_message_and_field_errors(
        status: u16,
        error: impl Into<String>,
        message: impl Into<String>,
        field_errors: HashMap<String, Vec<String>>,
    ) -> Self {
        ServiceResult::Err {
            status,
            error: error.into(),
            message: Some(message.into()),
            field_errors: Some(field_errors),
        }
    }

    pub fn is_ok(&self) -> bool {
        matches!(self, ServiceResult::Ok { .. })
    }
}
