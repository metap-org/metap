//! Mirrors `packages/core/src/core/crud/result.ts`.

use std::collections::HashMap;

#[derive(Debug, Clone)]
pub struct PageInfo {
    pub limit: i64,
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone)]
pub enum ServiceResult<T> {
    Ok { data: T, page: Option<PageInfo> },
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
        ServiceResult::Err { status, error: error.into(), message: None, field_errors: None }
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

    pub fn is_ok(&self) -> bool {
        matches!(self, ServiceResult::Ok { .. })
    }
}
