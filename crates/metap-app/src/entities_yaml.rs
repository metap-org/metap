//! Load `EntityDefinition`s from YAML files instead of hand-writing each one as a Rust module —
//! `docs/features/33-declarative-yaml-app-bootstrap.md`. `EntityDefinition` was already plain
//! `Serialize`/`Deserialize` data with no closures/trait objects (`metap-metadata`'s doc
//! comments) before this existed; the only thing missing was a loader. This does not register
//! anything into a `MetadataRegistry` itself — the caller still calls `.register(entity)?` per
//! entity exactly as it would for a hand-written one (`load_entity_definitions_from_dir` returns
//! a plain `Vec`), so YAML-loaded and Rust-authored entities are registered through the identical
//! path and can't drift.
//!
//! YAML uses the same field names as `EntityDefinition`'s own `camelCase` JSON shape (`tableName`,
//! `listViews`, ...) — the same shape `GET /metadata/openapi.json`/low-code export already use —
//! not a new convention invented for this file format.
//!
//! Deliberately NOT a place to attach custom Rust logic: `WorkflowTransition.guard`/`validator`
//! stay `PolicyCondition` (declarative, already YAML-representable), and a YAML-loaded entity's
//! *name* is all a binary needs to attach real custom code via `metap_infra::HandlerRegistry.on(
//! "<entityName>.record.created", ...)` in its own `main.rs` — no hook-name field was added to
//! this loader or to `EntityDefinition` itself. See that feature brief's "Rà soát" section for why
//! a YAML-embedded scripting/expression engine was deliberately rejected
//! (`docs/features/06-async-verification-pattern-and-lowcode-custom-logic.md`'s "Option C").

use std::path::Path;

use metap_metadata::EntityDefinition;

/// Deserializes one entity's YAML source into an `EntityDefinition` — split out from
/// `load_entity_definitions_from_dir` so it's unit-testable against string literals without
/// touching the filesystem.
pub fn parse_entity_yaml(source: &str) -> anyhow::Result<EntityDefinition> {
    serde_norway::from_str(source).map_err(|e| anyhow::anyhow!("invalid entity YAML: {e}"))
}

/// Reads every `*.yaml`/`*.yml` file directly inside `dir` (not recursive) and parses each into
/// an `EntityDefinition` — one entity per file, matching the one-Rust-module-per-entity
/// convention this replaces. Files are processed in sorted-filename order so a given directory's
/// registration order (and therefore any `RegistryError::AlreadyRegistered` duplicate-name error)
/// is deterministic across runs. A parse failure names the offending file rather than just the
/// serde error, since a boot-time failure here is otherwise hard to trace back to which file.
///
/// Does not validate against `MetadataRegistry` (no duplicate-name check across files, no
/// `compiler::validate`) — that happens for free when the caller calls `registry.register(entity)`
/// per returned entity, the same validation every hand-written entity already goes through.
pub fn load_entity_definitions_from_dir(dir: impl AsRef<Path>) -> anyhow::Result<Vec<EntityDefinition>> {
    let dir = dir.as_ref();
    let mut paths: Vec<_> = std::fs::read_dir(dir)
        .map_err(|e| anyhow::anyhow!("failed to read entity YAML directory {}: {e}", dir.display()))?
        .filter_map(|entry| entry.ok())
        .map(|entry| entry.path())
        .filter(|path| {
            path.extension()
                .and_then(|ext| ext.to_str())
                .is_some_and(|ext| ext.eq_ignore_ascii_case("yaml") || ext.eq_ignore_ascii_case("yml"))
        })
        .collect();
    paths.sort();

    paths
        .into_iter()
        .map(|path| {
            let source = std::fs::read_to_string(&path)
                .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", path.display()))?;
            parse_entity_yaml(&source).map_err(|e| anyhow::anyhow!("{}: {e}", path.display()))
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_a_minimal_entity_with_camel_case_keys() {
        let entity = parse_entity_yaml(
            r#"
name: crm.customers
label: Customers
tableName: records
fields:
  - name: fullName
    label: Full name
    kind: string
    required: true
listViews: []
"#,
        )
        .unwrap();
        assert_eq!(entity.name, "crm.customers");
        assert_eq!(entity.table_name, "records");
        assert_eq!(entity.fields.len(), 1);
        assert_eq!(entity.fields[0].name, "fullName");
    }

    #[test]
    fn rejects_malformed_yaml_with_a_readable_error() {
        let err = parse_entity_yaml("name: [unterminated").unwrap_err();
        assert!(err.to_string().contains("invalid entity YAML"));
    }

    #[test]
    fn loads_every_yaml_file_in_a_directory_in_sorted_order() {
        // `Uuid::new_v4()`, not `std::process::id()` — a securely-random, unpredictable name,
        // matching the convention every other `std::env::temp_dir()`-based test in this
        // workspace already uses (e.g. `metap-http/tests/http_server.rs`). A predictable name
        // under the shared system temp dir is exactly what semgrep's
        // `rust.lang.security.temp-dir.temp-dir` rule flags (symlink/race attacks on a
        // multi-user machine).
        let dir = std::env::temp_dir().join(format!("metap-app-entities-yaml-test-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(
            dir.join("b.yaml"),
            "name: crm.orders\nlabel: Orders\ntableName: records\nfields: []\nlistViews: []\n",
        )
        .unwrap();
        std::fs::write(
            dir.join("a.yml"),
            "name: crm.customers\nlabel: Customers\ntableName: records\nfields: []\nlistViews: []\n",
        )
        .unwrap();
        std::fs::write(dir.join("ignore.txt"), "not yaml").unwrap();

        let entities = load_entity_definitions_from_dir(&dir).unwrap();
        std::fs::remove_dir_all(&dir).unwrap();

        assert_eq!(entities.len(), 2);
        assert_eq!(entities[0].name, "crm.customers");
        assert_eq!(entities[1].name, "crm.orders");
    }
}
