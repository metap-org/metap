//! Mirrors `packages/core/src/core/permission/permission-snapshot.ts`: a per-call batch of
//! a tenant/entity's policies, loaded once and reused across a single `CrudService` call. The
//! *snapshot* itself is still built fresh per call (never held across calls) — but the *rows* it
//! is built from can now come from a TTL cache instead of a fresh `PolicyStore` query every
//! time, see `PermissionService::load_snapshot`/`crates/metap-cache`. This does not weaken the
//! "no stale permission data" property much: unlike role assignment (`user_roles`, never cached,
//! `crates/metap-http/src/auth.rs`), policy rows are the *rules*, changed rarely by an admin, not
//! per-request state — the same "ordinary config data, short TTL + explicit invalidation on
//! write" reasoning `metap-http::ContextAttributesCache` already applies to caller attributes.

use std::collections::HashMap;

use uuid::Uuid;

use crate::context::{EntityAction, PermissionDecision, RequestContext};
use crate::policy_condition::{evaluate_policies, PolicyVerdict};
use crate::policy_store::{PolicyRow, PolicyStore};

pub type JsonObject = serde_json::Map<String, serde_json::Value>;

pub struct PermissionSnapshot {
    field_policies: Vec<PolicyRow>,
    record_policies_by_action: HashMap<String, Vec<PolicyRow>>,
}

impl PermissionSnapshot {
    pub async fn load(store: &dyn PolicyStore, tenant_id: Uuid, entity: &str) -> anyhow::Result<Self> {
        let rows = store.load_all_policies(tenant_id, entity).await?;
        Ok(Self::from_rows(rows))
    }

    /// Builds a snapshot from already-fetched rows, whether they came straight from
    /// `PolicyStore` (`load`, above) or from `PermissionService`'s cache
    /// (`load_snapshot`/`crates/metap-cache`) — kept separate from `load` so the caching
    /// decision lives entirely in `PermissionService`, not duplicated here.
    pub fn from_rows(rows: Vec<PolicyRow>) -> Self {
        let field_policies: Vec<PolicyRow> = rows.iter().filter(|r| r.field.is_some()).cloned().collect();

        let mut record_policies_by_action: HashMap<String, Vec<PolicyRow>> = HashMap::new();
        for row in &rows {
            if row.field.is_none() && row.subject == "record" {
                record_policies_by_action
                    .entry(row.action.clone())
                    .or_default()
                    .push(row.clone());
            }
        }

        Self {
            field_policies,
            record_policies_by_action,
        }
    }

    pub fn get_record_policies(&self, action: EntityAction) -> &[PolicyRow] {
        self.record_policies_by_action
            .get(action.as_str())
            .map(|v| v.as_slice())
            .unwrap_or(&[])
    }

    /// Relation field names (`"project"`, not `"project.ownerId"`) that `action`'s record-level
    /// policies reference via a dotted attribute path — see
    /// `crate::policy_condition::required_relation_fields`'s doc comment for why this stays
    /// cheap when nothing needs it. `CrudService` calls this before evaluating record-level
    /// policies for a single-record operation, fetches only the named relations, and merges
    /// them onto the subject; `list()` has no equivalent (would need `QueryPlanner` JOIN
    /// support), so those conditions never resolve when pushed into SQL.
    pub fn required_relation_fields(&self, action: EntityAction) -> Vec<String> {
        crate::policy_condition::required_relation_fields(self.get_record_policies(action))
    }

    pub fn filter_readable_fields(&self, context: &RequestContext, record: &JsonObject) -> JsonObject {
        if context.is_admin() {
            return record.clone();
        }

        let mut read_policies_by_field: HashMap<&str, Vec<&PolicyRow>> = HashMap::new();
        for policy in &self.field_policies {
            if policy.action != "read" {
                continue;
            }
            let Some(field) = &policy.field else { continue };
            read_policies_by_field.entry(field.as_str()).or_default().push(policy);
        }

        let record_value = serde_json::Value::Object(record.clone());
        let mut result = JsonObject::new();
        for (key, value) in record {
            match read_policies_by_field.get(key.as_str()) {
                None => {
                    result.insert(key.clone(), value.clone());
                }
                Some(policies) => {
                    if evaluate_policies(policies.iter().copied(), context, Some(&record_value)).is_allowed() {
                        result.insert(key.clone(), value.clone());
                    }
                }
            }
        }
        result
    }

    pub fn writable_fields(
        &self,
        context: &RequestContext,
        all_field_names: &[String],
        existing_record: Option<&JsonObject>,
    ) -> Vec<String> {
        if context.is_admin() {
            return all_field_names.to_vec();
        }

        let mut write_policies_by_field: HashMap<&str, Vec<&PolicyRow>> = HashMap::new();
        for policy in &self.field_policies {
            if policy.action != "write" {
                continue;
            }
            let Some(field) = &policy.field else { continue };
            write_policies_by_field.entry(field.as_str()).or_default().push(policy);
        }

        let existing_value = existing_record.map(|r| serde_json::Value::Object(r.clone()));

        all_field_names
            .iter()
            .filter(|field| match write_policies_by_field.get(field.as_str()) {
                None => true,
                Some(policies) => {
                    evaluate_policies(policies.iter().copied(), context, existing_value.as_ref()).is_allowed()
                }
            })
            .cloned()
            .collect()
    }

    pub fn assert_writable_fields(
        &self,
        context: &RequestContext,
        payload_fields: &[String],
        existing_record: Option<&JsonObject>,
    ) -> PermissionDecision {
        if context.is_admin() {
            return PermissionDecision::allowed();
        }

        let writable = self.writable_fields(context, payload_fields, existing_record);
        let writable_set: std::collections::HashSet<&str> = writable.iter().map(String::as_str).collect();

        match payload_fields.iter().find(|f| !writable_set.contains(f.as_str())) {
            Some(denied_field) => {
                tracing::warn!(field = %denied_field, "denied: field not writable");
                PermissionDecision::forbidden_field(denied_field.clone())
            }
            None => PermissionDecision::allowed(),
        }
    }

    pub fn can_perform_record_condition(
        &self,
        context: &RequestContext,
        record: &JsonObject,
        action: EntityAction,
    ) -> PermissionDecision {
        let record_policies = self.get_record_policies(action);

        if context.is_admin() || record_policies.is_empty() {
            return PermissionDecision::allowed();
        }

        let record_value = serde_json::Value::Object(record.clone());
        match evaluate_policies(record_policies, context, Some(&record_value)) {
            PolicyVerdict::Allow => PermissionDecision::allowed(),
            PolicyVerdict::Deny => {
                tracing::warn!(
                    action = action.as_str(),
                    "denied: an explicit deny record-level policy matched"
                );
                PermissionDecision::forbidden()
            }
            PolicyVerdict::NoMatch => {
                tracing::warn!(
                    action = action.as_str(),
                    "denied: no record-level policy condition matched"
                );
                PermissionDecision::forbidden()
            }
        }
    }
}
