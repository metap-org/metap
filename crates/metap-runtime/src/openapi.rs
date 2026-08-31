//! The `insert(paths, path, item)` helper for building an OpenAPI `paths` map by hand — found
//! byte-identical in `metap-http`/`metap-lowcode-http`/`metap-control-http`'s own
//! `openapi_paths.rs` (each crate hand-writes its own static route fragments, same reasoning as
//! `metap_metadata::generate_openapi_document`: no Zod-equivalent reflection step exists to
//! derive these from). Only the 3-line insertion helper was identical across all 3 — the actual
//! path fragments each crate builds are entirely different, so those stay local.

use serde_json::{Map, Value};

pub fn insert(paths: &mut Map<String, Value>, path: &str, item: Value) {
    paths.insert(path.to_string(), item);
}
