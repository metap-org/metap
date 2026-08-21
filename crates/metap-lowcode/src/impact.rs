//! Migration-impact check for destructive metadata changes (`docs/roadmap.md` Phase 11 Phase
//! C, `docs/low-code-platform-v1.md`). Diffs a currently-published `LowCodeEntityDefinition`
//! against a draft about to be previewed/published and flags field-level changes that can leave
//! *existing* records in `records` inconsistent with the new shape — the generic `records` table
//! has no schema of its own (Phase A), so none of this is caught by a database constraint the
//! way an `ALTER TABLE` failure would catch it. Advisory only: `preview_publish` surfaces these
//! as warnings, nothing here blocks a publish — an operator may have a good reason (a field is
//! actually unused by any real record yet) and the platform has no way to know that without
//! actually querying tenant data, which is out of scope for a metadata-only check.

use std::collections::HashSet;

use metap_metadata::{EntityField, FieldKind};
use serde::Serialize;

use crate::definition::LowCodeEntityDefinition;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub enum ImpactKind {
    FieldRemoved,
    FieldKindChanged,
    FieldMadeRequired,
    FieldMadeUnique,
    EnumValueRemoved,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ImpactWarning {
    pub field: String,
    pub kind: ImpactKind,
    pub message: String,
}

/// `published` is the entity's current live definition, `draft` is what's about to become the
/// new one — only fields present in `published` are ever inspected, since a brand-new field
/// can't have existing data to be inconsistent with.
pub fn diff_impact(published: &LowCodeEntityDefinition, draft: &LowCodeEntityDefinition) -> Vec<ImpactWarning> {
    let draft_fields: std::collections::HashMap<&str, &EntityField> =
        draft.fields.iter().map(|f| (f.name.as_str(), f)).collect();

    let mut warnings = Vec::new();
    for old_field in &published.fields {
        let Some(new_field) = draft_fields.get(old_field.name.as_str()) else {
            warnings.push(ImpactWarning {
                field: old_field.name.clone(),
                kind: ImpactKind::FieldRemoved,
                message: format!(
                    "Field \"{}\" is removed. Existing records keep this value in the underlying jsonb column, but it becomes unreachable through the UI, filters, and validation.",
                    old_field.name
                ),
            });
            continue;
        };

        if new_field.kind != old_field.kind {
            warnings.push(ImpactWarning {
                field: old_field.name.clone(),
                kind: ImpactKind::FieldKindChanged,
                message: format!(
                    "Field \"{}\" kind changes from {:?} to {:?}. Existing values written under the old kind are not converted or re-validated.",
                    old_field.name, old_field.kind, new_field.kind
                ),
            });
        }

        if new_field.required == Some(true) && old_field.required != Some(true) {
            warnings.push(ImpactWarning {
                field: old_field.name.clone(),
                kind: ImpactKind::FieldMadeRequired,
                message: format!(
                    "Field \"{}\" becomes required. Existing records missing it are not retroactively rejected — only the next write to each one is.",
                    old_field.name
                ),
            });
        }

        if new_field.unique == Some(true) && old_field.unique != Some(true) {
            warnings.push(ImpactWarning {
                field: old_field.name.clone(),
                kind: ImpactKind::FieldMadeUnique,
                message: format!(
                    "Field \"{}\" becomes unique. If existing records already have duplicate values, the reconciled index build will fail or leave the constraint unenforced until the duplicates are resolved.",
                    old_field.name
                ),
            });
        }

        if old_field.kind == FieldKind::Enum {
            let old_values: HashSet<&str> = old_field.enum_values.iter().flatten().map(String::as_str).collect();
            let new_values: HashSet<&str> = new_field.enum_values.iter().flatten().map(String::as_str).collect();
            for removed in old_values.difference(&new_values) {
                warnings.push(ImpactWarning {
                    field: old_field.name.clone(),
                    kind: ImpactKind::EnumValueRemoved,
                    message: format!(
                        "Field \"{}\" no longer allows enum value \"{removed}\". Existing records holding it will fail validation on their next write.",
                        old_field.name
                    ),
                });
            }
        }
    }
    warnings
}
