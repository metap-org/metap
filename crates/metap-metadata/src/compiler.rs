//! Mirrors `packages/core/src/core/metadata/metadata-compiler.ts`: boot-time structural
//! validation and a deterministic content hash, both driven purely by entity metadata
//! (never request input) — same contract, same issue messages where the shape allows it.

use std::collections::HashSet;

use sha2::{Digest, Sha256};

use crate::computed::expression_tokens;
use crate::entity::{EntityDefinition, EntityField, EntityListView, EntityWorkflow};

/// Always present on every entity's underlying `records` row regardless of declared
/// fields — see `QueryPlanner`'s `sortableFields`/`fieldExpression` in the TS codebase,
/// which resolve these to real DB columns independent of `entity.fields`.
const IMPLICIT_SYSTEM_FIELDS: [&str; 2] = ["createdAt", "updatedAt"];

#[derive(Debug, Clone)]
pub struct MetadataValidationError {
    pub entity: String,
    pub issues: Vec<String>,
}

impl std::fmt::Display for MetadataValidationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "Invalid metadata for entity \"{}\": {}",
            self.entity,
            self.issues.join("; ")
        )
    }
}

impl std::error::Error for MetadataValidationError {}

#[derive(serde::Serialize)]
struct HashShape<'a> {
    label: &'a str,
    fields: &'a [EntityField],
    #[serde(rename = "listViews")]
    list_views: &'a [EntityListView],
    #[serde(skip_serializing_if = "Option::is_none")]
    workflow: Option<&'a EntityWorkflow>,
}

/// Deterministic SHA-256 over the entity's shape — `label`/`fields`/`listViews`/`workflow`,
/// including each transition's `guard` since `entity.rs`'s `WorkflowTransition::guard` un-skip
/// (2026-08-17) — a guard-only edit now correctly bumps the hash / `version` at
/// `GET /metadata/entities`, where before it silently didn't. `serde_json::Value`'s object map is a
/// `BTreeMap` by default (no `preserve_order` feature enabled anywhere in this workspace),
/// so `to_string` on a `Value` built via `to_value` emits keys in sorted order at every
/// level — the same guarantee `stableStringify`'s explicit `Object.keys(value).sort()`
/// gives the TS implementation. Array element order is preserved unsorted in both.
pub fn hash(entity: &EntityDefinition) -> anyhow::Result<String> {
    let shape = HashShape {
        label: &entity.label,
        fields: &entity.fields,
        list_views: &entity.list_views,
        workflow: entity.workflow.as_ref(),
    };
    let value = serde_json::to_value(&shape)?;
    let stable_json = serde_json::to_string(&value)?;
    let digest = Sha256::digest(stable_json.as_bytes());
    Ok(hex::encode(digest))
}

pub fn validate(entity: &EntityDefinition) -> Result<(), MetadataValidationError> {
    let mut issues = Vec::new();
    let mut field_names: HashSet<&str> = HashSet::new();

    // `field.name` is interpolated directly into SQL just like `table_name` below — as a real
    // column identifier (`metap-reconciler::compile`'s table-per-entity DDL, e.g.
    // `format!("\"{}\"", field.name)`) or as a JSONB path literal
    // (`data ->> '{field.name}'`, `metap-crud::find_referencing_record`) — but unlike
    // `table_name` had no charset check at all until this validation existed. Since a
    // low-code-authored entity's field names come from admin-supplied metadata (not a
    // developer's own source), an unvalidated name is a live SQL-injection vector at every one
    // of those interpolation sites. Every field name in this codebase is camelCase
    // (`customerId`, `dueDate`, ...), so this whitelist matches existing usage rather than
    // tightening it.
    fn is_safe_field_name(name: &str) -> bool {
        !name.is_empty()
            && name.chars().next().is_some_and(|c| c.is_ascii_alphabetic())
            && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
    }

    for field in &entity.fields {
        if !field_names.insert(field.name.as_str()) {
            issues.push(format!("duplicate field name \"{}\"", field.name));
        }

        if !is_safe_field_name(&field.name) {
            issues.push(format!(
                "field name \"{}\" must match ^[A-Za-z][A-Za-z0-9_]*$",
                field.name
            ));
        }

        if matches!(field.kind, crate::entity::FieldKind::Enum)
            && field.enum_values.as_ref().map(|v| v.is_empty()).unwrap_or(true)
        {
            issues.push(format!(
                "field \"{}\" is kind \"enum\" but declares no enumValues",
                field.name
            ));
        }

        if matches!(field.kind, crate::entity::FieldKind::Reference) && field.ref_entity.is_none() {
            issues.push(format!(
                "field \"{}\" is kind \"reference\" but declares no refEntity",
                field.name
            ));
        }

        let is_numeric = matches!(
            field.kind,
            crate::entity::FieldKind::Number | crate::entity::FieldKind::Money
        );
        if !is_numeric && (field.min.is_some() || field.max.is_some()) {
            issues.push(format!(
                "field \"{}\" declares min/max but is kind \"{:?}\", not number/money",
                field.name, field.kind
            ));
        }
        if let (Some(min), Some(max)) = (field.min, field.max) {
            if min > max {
                issues.push(format!(
                    "field \"{}\" has min ({min}) greater than max ({max})",
                    field.name
                ));
            }
        }

        if !matches!(field.kind, crate::entity::FieldKind::String)
            && (field.min_length.is_some() || field.max_length.is_some())
        {
            issues.push(format!(
                "field \"{}\" declares minLength/maxLength but is kind \"{:?}\", not string",
                field.name, field.kind
            ));
        }
        if let (Some(min_length), Some(max_length)) = (field.min_length, field.max_length) {
            if min_length > max_length {
                issues.push(format!(
                    "field \"{}\" has minLength ({min_length}) greater than maxLength ({max_length})",
                    field.name
                ));
            }
        }
    }

    // Second pass for `computed` — needs `field_names` (which computed fields' `dependsOn`
    // reference) fully populated by the loop above first, and needs to know which OTHER fields
    // are themselves computed (no chaining in v1, see `EntityField.computed`'s doc comment), so
    // it can't be folded into the first loop.
    let computed_field_names: std::collections::HashSet<&str> = entity
        .fields
        .iter()
        .filter(|f| f.computed.is_some())
        .map(|f| f.name.as_str())
        .collect();
    for field in &entity.fields {
        let Some(computed) = &field.computed else { continue };

        if !matches!(field.kind, crate::entity::FieldKind::String) {
            issues.push(format!(
                "field \"{}\" declares computed but is kind \"{:?}\", not string (v1 only supports \
                 string template expressions)",
                field.name, field.kind
            ));
        }
        if field.required == Some(true) {
            issues.push(format!(
                "field \"{}\" declares computed and required — the server always overwrites a \
                 computed field's value, required makes no sense here",
                field.name
            ));
        }
        if field.searchable == Some(true) || field.sortable == Some(true) {
            issues.push(format!(
                "field \"{}\" declares computed and searchable/sortable — not supported in v1, \
                 there is no materialize step yet (see docs/features/13-computed-derived-field.md)",
                field.name
            ));
        }

        for dep in &computed.depends_on {
            if dep == &field.name {
                issues.push(format!(
                    "field \"{}\" has computed.dependsOn referencing itself",
                    field.name
                ));
            } else if computed_field_names.contains(dep.as_str()) {
                issues.push(format!(
                    "field \"{}\" has computed.dependsOn referencing another computed field \"{dep}\" — \
                     chaining computed fields is not supported in v1",
                    field.name
                ));
            } else if !field_names.contains(dep.as_str()) {
                issues.push(format!(
                    "field \"{}\" has computed.dependsOn referencing unknown field \"{dep}\"",
                    field.name
                ));
            }
        }

        for token in expression_tokens(&computed.expression) {
            if !computed.depends_on.iter().any(|d| d == token) {
                issues.push(format!(
                    "field \"{}\" has computed.expression referencing \"{{{token}}}\" which is not \
                     listed in dependsOn",
                    field.name
                ));
            }
        }
    }

    let is_known_field = |name: &str| -> bool { field_names.contains(name) || IMPLICIT_SYSTEM_FIELDS.contains(&name) };

    for list_view in &entity.list_views {
        for field_name in &list_view.fields {
            if !is_known_field(field_name) {
                issues.push(format!(
                    "listView \"{}\" references unknown field \"{}\"",
                    list_view.name, field_name
                ));
            }
        }
        for field_name in &list_view.filters {
            if !is_known_field(field_name) {
                issues.push(format!(
                    "listView \"{}\" filters on unknown field \"{}\"",
                    list_view.name, field_name
                ));
            }
        }
        if let Some(default_sort) = &list_view.default_sort {
            let sort_field = default_sort.strip_prefix('-').unwrap_or(default_sort);
            if !is_known_field(sort_field) {
                issues.push(format!(
                    "listView \"{}\" defaultSort references unknown field \"{}\"",
                    list_view.name, sort_field
                ));
            }
        }
    }

    if let Some(workflow) = &entity.workflow {
        if !field_names.contains(workflow.state_field.as_str()) {
            issues.push(format!(
                "workflow.stateField \"{}\" is not a declared field",
                workflow.state_field
            ));
        }
        if workflow.initial_state.is_empty() {
            issues.push("workflow.initialState must be a non-empty string".to_string());
        }
        for state in &workflow.terminal_states {
            if state.is_empty() {
                issues.push("workflow.terminalStates contains an empty string".to_string());
            }
        }

        let mut seen_transition_keys: HashSet<String> = HashSet::new();
        for transition in &workflow.transitions {
            if transition.from.is_empty() || transition.to.is_empty() || transition.action.is_empty() {
                issues.push(format!(
                    "workflow transition is missing from/to/action: {{\"from\":\"{}\",\"to\":\"{}\",\"action\":\"{}\"}}",
                    transition.from, transition.to, transition.action
                ));
                continue;
            }
            let key = format!("{}::{}", transition.from, transition.action);
            if !seen_transition_keys.insert(key) {
                issues.push(format!(
                    "duplicate transition action \"{}\" from state \"{}\"",
                    transition.action, transition.from
                ));
            }

            if let Some(set_fields) = &transition.set_fields {
                for field_name in set_fields.keys() {
                    if field_name == &workflow.state_field {
                        issues.push(format!(
                            "transition \"{}\" setFields sets \"{field_name}\", which is stateField — \
                             use `to` instead",
                            transition.action
                        ));
                    } else if !is_known_field(field_name) {
                        issues.push(format!(
                            "transition \"{}\" setFields references unknown field \"{field_name}\"",
                            transition.action
                        ));
                    }
                }
            }
        }

        // Cross-check every state name workflow metadata mentions against `stateField`'s own
        // `enumValues` — found live (`AUDIT_2.md`): before this, a typo'd `from`/`to`/
        // `initialState`/`terminalStates` entry (e.g. `"actvie"` for `"active"`) compiled and
        // validated cleanly, then `CrudService::transition` wrote that exact typo'd value
        // straight into the record's state column with no further check
        // (`crud_service.rs`'s `to_state = transition.to.clone()`) — a permanently "broken"
        // status no other transition could ever match `from` against again. Only checkable when
        // `stateField` is actually an `Enum` with declared `enumValues` — a plain `String`
        // state field (never seen in this codebase, but not structurally forbidden) has no
        // fixed vocabulary to check against, so this degrades to a no-op rather than an error.
        let state_enum_values = entity
            .fields
            .iter()
            .find(|f| f.name == workflow.state_field)
            .filter(|f| matches!(f.kind, crate::entity::FieldKind::Enum))
            .and_then(|f| f.enum_values.as_ref());
        if let Some(enum_values) = state_enum_values {
            let enum_set: HashSet<&str> = enum_values.iter().map(String::as_str).collect();
            if !workflow.initial_state.is_empty() && !enum_set.contains(workflow.initial_state.as_str()) {
                issues.push(format!(
                    "workflow.initialState \"{}\" is not one of stateField \"{}\"'s enumValues",
                    workflow.initial_state, workflow.state_field
                ));
            }
            for state in &workflow.terminal_states {
                if !state.is_empty() && !enum_set.contains(state.as_str()) {
                    issues.push(format!(
                        "workflow.terminalStates contains \"{state}\", not one of stateField \"{}\"'s enumValues",
                        workflow.state_field
                    ));
                }
            }
            for transition in &workflow.transitions {
                if transition.from.is_empty() || transition.to.is_empty() {
                    continue; // already reported above
                }
                if !enum_set.contains(transition.from.as_str()) {
                    issues.push(format!(
                        "workflow transition \"{}\" has from-state \"{}\", not one of stateField \"{}\"'s enumValues",
                        transition.action, transition.from, workflow.state_field
                    ));
                }
                if !enum_set.contains(transition.to.as_str()) {
                    issues.push(format!(
                        "workflow transition \"{}\" has to-state \"{}\", not one of stateField \"{}\"'s enumValues",
                        transition.action, transition.to, workflow.state_field
                    ));
                }
            }
        }
    }

    // `table_name` is interpolated directly into SQL (`crates/metap-crud`, `crates/metap-query`)
    // since Postgres can't parameterize an identifier — validate it here, at the trust boundary
    // where entities are registered, rather than trusting it blindly at query time. A dedicated
    // table-per-entity table is schema-qualified (`metap_reconciler::qualified_table_name_for`,
    // e.g. `"entities.jira_issues"` — never `public`, see that function's doc comment for why),
    // so each `.`-separated segment is checked against the same identifier charset separately.
    fn is_safe_ident_segment(segment: &str) -> bool {
        !segment.is_empty()
            && segment.chars().next().is_some_and(|c| c.is_ascii_lowercase())
            && segment
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
    }
    let table_name_ok = entity.table_name == "records"
        || entity.table_name.split('.').all(is_safe_ident_segment) && entity.table_name.contains('.');
    if !table_name_ok {
        issues.push(format!(
            "tableName \"{}\" must be \"records\" or a schema-qualified ^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$ name",
            entity.table_name
        ));
    }

    // `entity.name` had no charset check at all until this validation existed — same gap
    // `field.name` had (see that check's doc comment above), just harder to hit accidentally
    // since a code-authored entity's name is a Rust string literal, not admin-supplied metadata.
    // A low-code-authored entity's name *is* admin-supplied, though, and it flows into identifier
    // construction the same way `table_name` does: `metap_reconciler::table_name_for` (a bare
    // `.replace('.', "_")`, no charset check of its own) and
    // `metap_peripherals::index_reconciler::build_index_name`. Currently safe only because every
    // identifier built from it is `quote_ident`-quoted before reaching SQL
    // (`metap_reconciler::executor`) — this closes the gap at the actual trust boundary instead
    // of relying on that downstream discipline holding forever. Same dotted-lowercase shape every
    // real entity name in this codebase already uses (`crm.customers`, `jira.issues`, ...).
    let entity_name_ok = entity.name.contains('.') && entity.name.split('.').all(is_safe_ident_segment);
    if !entity_name_ok {
        issues.push(format!(
            "entity name \"{}\" must be a dotted ^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$ name",
            entity.name
        ));
    }

    if issues.is_empty() {
        Ok(())
    } else {
        Err(MetadataValidationError {
            entity: entity.name.clone(),
            issues,
        })
    }
}

#[cfg(test)]
mod tests;
