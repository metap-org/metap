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

    /// Whether this caller's **record-level** decision for `action` depends on the record's own
    /// current values — at least one record-level policy that passes their role gate carries a
    /// condition, so the same caller gets a different answer as the record changes.
    ///
    /// The same hazard as `record_state_dependent_read_fields` below, one level up and with a
    /// wider blast radius: record-level access gates the audit trail as a whole, so a caller who
    /// can steer the record into satisfying that condition gains the record's *entire* history,
    /// including values recorded while they had no access to it at all. Unlike the field-level
    /// case this needs no malicious edit to bite — a record that legitimately moves between
    /// owners/departments over time carries its previous owner's values in that history.
    ///
    /// Conservative on purpose, and deliberately over-broad in one case: a caller who also holds
    /// an unconditional grant is reported state-dependent anyway, even though the unconditional
    /// grant alone would make their access stable. Narrowing that would mean reasoning about
    /// Allow/Deny precedence across the conditional and unconditional sets separately — more
    /// logic to get wrong in a security check, for a narrow gain.
    pub fn record_access_is_state_dependent(&self, context: &RequestContext, action: EntityAction) -> bool {
        if context.is_admin() {
            return false;
        }
        self.get_record_policies(action).iter().any(|policy| {
            policy.condition.is_some()
                && crate::policy_condition::role_gate_passed(policy.roles.as_deref(), context.roles.as_deref())
        })
    }

    /// Field names whose `read` decision depends on the **record's own current values** — at
    /// least one of that field's read policies is record-subject *and* carries a condition, so
    /// the same caller gets a different answer as the record changes.
    ///
    /// `filter_readable_fields` deliberately answers for the record it is handed, which is right
    /// for the ordinary read path: the record it masks *is* the record being returned. A caller
    /// serving data about states the record no longer has — the audit trail — cannot use that
    /// answer, because the subject of the check is one the caller can move: editing the field a
    /// policy conditions on (a field they may legitimately write) flips the decision and
    /// retroactively unmasks history recorded while it was closed. Confirmed live, not
    /// theoretical: see `audit_events.rs` and its regression test.
    ///
    /// Evaluating each audit entry against its *own* historical record state would be the
    /// precise answer, but `metadata.audit_trail_entries` stores only a per-entry `diff`, never
    /// a full state snapshot, so that state cannot be reconstructed — and reconstructing it by
    /// replaying diffs would be wrong exactly when the audit trail is incomplete, which it is
    /// allowed to be (the write is best-effort by design). Excluding these fields is therefore
    /// the conservative answer available today, not the ideal one.
    pub fn record_state_dependent_read_fields(&self) -> std::collections::HashSet<String> {
        self.field_policies
            .iter()
            .filter(|policy| policy.action == "read" && policy.subject == "record" && policy.condition.is_some())
            .filter_map(|policy| policy.field.clone())
            .collect()
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

    /// **Allow-by-default**, unlike entity-level permission (`PermissionService::can_read_entity`
    /// etc., which is deny-by-default — `NoMatch` → forbidden). A field with no read policy at
    /// all is included for anyone who can already read the entity; only a field that has a read
    /// policy which evaluates to not-allowed gets masked out. Deliberate (mirrors the original
    /// TS behavior), but easy for a new downstream project to get wrong: a newly added field is
    /// world-readable to anyone who can read the entity until someone writes a field policy for
    /// it, not private-until-granted. Found undocumented in an architecture audit
    /// (`../metap-docs/docs/audits/03-metap-core-architecture-audit.md` finding #11, 2026-09-02).
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

    /// **Allow-by-default**, same asymmetry as `filter_readable_fields` above — a field with no
    /// write policy is writable to anyone who can already update the entity.
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

    /// **Allow-by-default when NO record-level policy exists for `action`** (`record_policies.is_empty()`
    /// below) — same asymmetry as `filter_readable_fields`/`writable_fields` above, entity-level
    /// permission already gated this call and stays the deny-by-default layer. Once at least one
    /// record-level policy for `action` DOES exist, this flips to deny-by-default within that
    /// set: `NoMatch` (no policy's condition matched this specific record) is forbidden, not
    /// allowed — unlike the field-level case, which allows a field with no matching policy.
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
