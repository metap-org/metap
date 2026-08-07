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

#[derive(Default)]
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

    pub fn get_entity(&self, name: &str) -> Option<&EntityDefinition> {
        self.entities.get(name)
    }

    pub fn validate_references(&self) -> Result<(), MetadataValidationError> {
        for entity in self.entities.values() {
            let mut issues = Vec::new();
            for field in &entity.fields {
                if !matches!(field.kind, FieldKind::Reference) {
                    continue;
                }
                let Some(ref_entity_name) = &field.ref_entity else { continue };

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
                return Err(MetadataValidationError { entity: entity.name.clone(), issues });
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
        registry.register(entity("crm.customers", vec![field("name", FieldKind::String)])).unwrap();
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
    fn list_entities_includes_deterministic_version_hash() {
        let mut registry = MetadataRegistry::new();
        registry.register(entity("crm.customers", vec![field("name", FieldKind::String)])).unwrap();
        let summaries = registry.list_entities();
        assert_eq!(summaries.len(), 1);
        assert!(!summaries[0].version.is_empty());
    }
}
