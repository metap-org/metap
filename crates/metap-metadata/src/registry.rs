//! Mirrors `packages/core/src/core/metadata/metadata-registry.ts`: a read-only-after-boot
//! registry populated once by whichever `apps/<module>` calls it (see `entity.rs`'s note on
//! why entity registration stays an application-layer concern, not `packages/core`'s).

use std::collections::HashMap;

use crate::compiler::{self, MetadataValidationError};
use crate::entity::{EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind};

#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct EntitySummary {
    pub name: String,
    pub label: String,
    pub fields: Vec<EntityField>,
    pub list_views: Vec<EntityListView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub workflow: Option<EntityWorkflow>,
    pub version: String,
}

#[derive(Debug)]
pub enum RegistryError {
    AlreadyRegistered(String),
    Validation(MetadataValidationError),
}

impl std::fmt::Display for RegistryError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RegistryError::AlreadyRegistered(name) => {
                write!(f, "Entity already registered: {name}")
            }
            RegistryError::Validation(err) => write!(f, "{err}"),
        }
    }
}

impl std::error::Error for RegistryError {}

impl From<MetadataValidationError> for RegistryError {
    fn from(err: MetadataValidationError) -> Self {
        RegistryError::Validation(err)
    }
}

/// One entity factory submitted via `submit_entity!` (usually through the `metap` facade,
/// `metap::submit_entity!` — see that macro's own doc comment below), collected at link time
/// via `inventory` so a downstream binary can register every entity its own crate defines with
/// one `MetadataRegistry::register_all_submitted()` call instead of one
/// `registry.register(x_entity())?` line per entity — keeping the entity list in exactly one
/// place (wherever `submit_entity!` is invoked, normally right next to each entity's own
/// definition function) instead of two (a hand-written `mod` list plus a matching, easy-to-
/// forget `register` call in `main.rs`).
pub struct EntityFactory(pub fn() -> EntityDefinition);

inventory::collect!(EntityFactory);

#[doc(hidden)]
pub mod __private {
    pub use inventory;
}

/// Registers `$factory` (a `fn() -> EntityDefinition`, e.g. an entity module's own
/// `zone_entity` function) for pickup by `MetadataRegistry::register_all_submitted()`. Call
/// once per entity, anywhere compiled into the final binary — an entity module that is never
/// referenced by `main.rs` still gets picked up as long as its parent `mod` is declared
/// somewhere reachable, since `mod` declarations compile a module whether or not anything
/// calls into it. `inventory`'s submission runs via a pre-`main` constructor, so no explicit
/// init call is needed beyond declaring the module.
#[macro_export]
macro_rules! submit_entity {
    ($factory:path) => {
        $crate::registry::__private::inventory::submit! {
            $crate::registry::EntityFactory($factory)
        }
    };
}

#[derive(Debug, Default, Clone)]
pub struct MetadataRegistry {
    entities: HashMap<String, EntityDefinition>,
}

impl MetadataRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn register(&mut self, entity: EntityDefinition) -> Result<(), RegistryError> {
        if self.entities.contains_key(&entity.name) {
            return Err(RegistryError::AlreadyRegistered(entity.name.clone()));
        }
        compiler::validate(&entity)?;
        self.entities.insert(entity.name.clone(), entity);
        Ok(())
    }

    /// Registers every entity submitted via `submit_entity!` anywhere in this binary's
    /// dependency graph — the auto-discovery alternative to calling `register` once per entity
    /// by hand. Iteration order is link order, not declaration order; two entities submitted
    /// under the same name still fail the same way `register`'s own duplicate check does, just
    /// surfaced here instead of at a hand-written call site.
    pub fn register_all_submitted(&mut self) -> Result<(), RegistryError> {
        for factory in inventory::iter::<EntityFactory> {
            self.register((factory.0)())?;
        }
        Ok(())
    }

    pub fn get_entity(&self, name: &str) -> Option<&EntityDefinition> {
        self.entities.get(name)
    }

    /// Clones this registry and registers `extra` on top of it — used to merge a
    /// code-authored base registry with entities loaded from another source (`metap-lowcode`'s
    /// DB-authored entities) without either source knowing about the other. Fails the same way
    /// `register` does: a duplicate name (against `self` or within `extra`) or a shape
    /// validation failure aborts the whole merge rather than partially applying it.
    pub fn merge_with(&self, extra: Vec<EntityDefinition>) -> Result<MetadataRegistry, RegistryError> {
        let mut merged = MetadataRegistry {
            entities: self.entities.clone(),
        };
        for entity in extra {
            merged.register(entity)?;
        }
        Ok(merged)
    }

    pub fn validate_references(&self) -> Result<(), MetadataValidationError> {
        for entity in self.entities.values() {
            let mut issues = Vec::new();
            for field in &entity.fields {
                if !matches!(field.kind, FieldKind::Reference) {
                    continue;
                }
                let Some(ref_entity_name) = &field.ref_entity else {
                    continue;
                };

                match self.entities.get(ref_entity_name) {
                    None => issues.push(format!(
                        "field \"{}\" references unknown entity \"{}\"",
                        field.name, ref_entity_name
                    )),
                    Some(target) => {
                        if let Some(ref_display_field) = &field.ref_display_field {
                            if !target.fields.iter().any(|f| &f.name == ref_display_field) {
                                issues.push(format!(
                                    "field \"{}\" has refDisplayField \"{}\" which does not exist on \"{}\"",
                                    field.name, ref_display_field, ref_entity_name
                                ));
                            }
                        }
                    }
                }
            }
            if !issues.is_empty() {
                return Err(MetadataValidationError {
                    entity: entity.name.clone(),
                    issues,
                });
            }
        }
        Ok(())
    }

    pub fn get_entity_metadata(&self, name: &str) -> Option<EntitySummary> {
        self.entities.get(name).map(|e| self.to_metadata(e))
    }

    pub fn list_entities(&self) -> Vec<EntitySummary> {
        self.entities.values().map(|e| self.to_metadata(e)).collect()
    }

    fn to_metadata(&self, entity: &EntityDefinition) -> EntitySummary {
        EntitySummary {
            name: entity.name.clone(),
            label: entity.label.clone(),
            fields: entity.fields.clone(),
            list_views: entity.list_views.clone(),
            workflow: entity.workflow.clone(),
            // Hashing a plain struct of String/bool/Vec fields cannot fail in practice
            // (unlike JS's NaN/undefined edge cases) — panic rather than silently emit a
            // wrong/empty version if that assumption is ever violated.
            version: compiler::hash(entity).expect("hashing entity metadata cannot fail"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::entity::EntityField;

    fn field(name: &str, kind: FieldKind) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
        }
    }

    fn entity(name: &str, fields: Vec<EntityField>) -> EntityDefinition {
        EntityDefinition {
            name: name.to_string(),
            label: name.to_string(),
            table_name: "records".to_string(),
            fields,
            list_views: vec![],
            workflow: None,
        }
    }

    #[test]
    fn register_then_get_round_trips() {
        let mut registry = MetadataRegistry::new();
        registry
            .register(entity("crm.customers", vec![field("name", FieldKind::String)]))
            .unwrap();
        assert!(registry.get_entity("crm.customers").is_some());
        assert!(registry.get_entity("crm.unknown").is_none());
    }

    #[test]
    fn duplicate_registration_is_rejected() {
        let mut registry = MetadataRegistry::new();
        registry.register(entity("crm.customers", vec![])).unwrap();
        let err = registry.register(entity("crm.customers", vec![])).unwrap_err();
        assert!(matches!(err, RegistryError::AlreadyRegistered(_)));
    }

    #[test]
    fn validate_references_rejects_unknown_ref_entity() {
        let mut registry = MetadataRegistry::new();
        let mut f = field("parent", FieldKind::Reference);
        f.ref_entity = Some("crm.ghost".to_string());
        registry.register(entity("crm.customers", vec![f])).unwrap();
        let err = registry.validate_references().unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("unknown entity \"crm.ghost\"")));
    }

    #[test]
    fn validate_references_accepts_known_ref_entity_and_display_field() {
        let mut registry = MetadataRegistry::new();
        registry
            .register(entity("crm.vendors", vec![field("name", FieldKind::String)]))
            .unwrap();
        let mut f = field("vendor", FieldKind::Reference);
        f.ref_entity = Some("crm.vendors".to_string());
        f.ref_display_field = Some("name".to_string());
        registry.register(entity("crm.customers", vec![f])).unwrap();
        assert!(registry.validate_references().is_ok());
    }

    #[test]
    fn validate_references_rejects_unknown_ref_display_field() {
        let mut registry = MetadataRegistry::new();
        registry
            .register(entity("test.owners", vec![field("nickname_typo", FieldKind::String)]))
            .unwrap();
        let mut f = field("owner_id", FieldKind::Reference);
        f.ref_entity = Some("test.owners".to_string());
        f.ref_display_field = Some("nickname".to_string());
        registry.register(entity("test.widgets", vec![f])).unwrap();
        let err = registry.validate_references().unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("refDisplayField \"nickname\"")));
    }

    #[test]
    fn merge_with_combines_entities_from_both_sources() {
        let mut base = MetadataRegistry::new();
        base.register(entity("crm.customers", vec![field("name", FieldKind::String)]))
            .unwrap();

        let merged = base
            .merge_with(vec![entity("lowcode.demo", vec![field("title", FieldKind::String)])])
            .unwrap();

        assert!(merged.get_entity("crm.customers").is_some());
        assert!(merged.get_entity("lowcode.demo").is_some());
        // The base registry itself is untouched by the merge.
        assert!(base.get_entity("lowcode.demo").is_none());
    }

    #[test]
    fn merge_with_rejects_name_collision_with_base() {
        let mut base = MetadataRegistry::new();
        base.register(entity("crm.customers", vec![])).unwrap();

        let err = base.merge_with(vec![entity("crm.customers", vec![])]).unwrap_err();
        assert!(matches!(err, RegistryError::AlreadyRegistered(_)));
    }

    fn submitted_entity_a() -> EntityDefinition {
        entity("test.submitted_a", vec![field("name", FieldKind::String)])
    }
    fn submitted_entity_b() -> EntityDefinition {
        entity("test.submitted_b", vec![field("name", FieldKind::String)])
    }
    crate::submit_entity!(submitted_entity_a);
    crate::submit_entity!(submitted_entity_b);

    #[test]
    fn register_all_submitted_picks_up_every_submit_entity_call() {
        let mut registry = MetadataRegistry::new();
        registry.register_all_submitted().unwrap();
        assert!(registry.get_entity("test.submitted_a").is_some());
        assert!(registry.get_entity("test.submitted_b").is_some());
    }

    #[test]
    fn list_entities_includes_deterministic_version_hash() {
        let mut registry = MetadataRegistry::new();
        registry
            .register(entity("crm.customers", vec![field("name", FieldKind::String)]))
            .unwrap();
        let summaries = registry.list_entities();
        assert_eq!(summaries.len(), 1);
        assert!(!summaries[0].version.is_empty());
    }
}
