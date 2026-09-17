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
use crate::policy_condition::{
    evaluate_condition, evaluate_policies, role_gate_passed, ConditionResult, PolicyVerdict,
};
use crate::policy_store::{PolicyEffect, PolicyRow, PolicyStore};

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

    /// Whether this caller's **record-level** decision for `action` can come out differently
    /// depending on the record's own values — i.e. whether the caller can change the answer by
    /// changing the record.
    ///
    /// The hazard this guards is `record_state_dependent_read_fields` below, one level up and
    /// with a wider blast radius: record-level access gates the audit trail as a whole, so a
    /// caller who can steer the record into satisfying a condition gains its *entire* history,
    /// including values recorded while they had no access at all. Unlike the field-level case
    /// this bites without any malicious edit too — a record that legitimately moves between
    /// owners or departments carries its previous owner's values in that history.
    ///
    /// Answered exactly rather than approximated: see `verdict_is_record_dependent`. An earlier
    /// version treated the mere existence of a conditional policy as decisive, which over-masked
    /// a caller who also held an unconditional grant that settles the verdict whatever the
    /// record says.
    pub fn record_access_is_state_dependent(&self, context: &RequestContext, action: EntityAction) -> bool {
        if context.is_admin() {
            return false;
        }
        verdict_is_record_dependent(self.get_record_policies(action), context)
    }

    /// Field names whose `read` decision can come out differently depending on the record's own
    /// values, for this caller.
    ///
    /// `filter_readable_fields` deliberately answers for the record it is handed, which is right
    /// for the ordinary read path: the record it masks *is* the record being returned. A caller
    /// serving data about states the record no longer has — the audit trail — cannot use that
    /// answer, because the subject of the check is one the caller can move: editing the field a
    /// policy conditions on (a field they may legitimately write) flips the decision and
    /// retroactively unmasks history recorded while it was closed. Confirmed live, not
    /// theoretical: see `crud_service/audit_events.rs` and its regression tests.
    ///
    /// Evaluating each audit entry against its *own* historical record state would be the
    /// precise answer, but `metadata.audit_trail_entries` stores only a per-entry `diff`, never
    /// a full state snapshot, so that state cannot be reconstructed — and reconstructing it by
    /// replaying diffs would be wrong exactly when the audit trail is incomplete, which it is
    /// allowed to be (the write is best-effort by design). Excluding these fields is therefore
    /// the conservative answer available today, not the ideal one.
    pub fn record_state_dependent_read_fields(&self, context: &RequestContext) -> std::collections::HashSet<String> {
        if context.is_admin() {
            return std::collections::HashSet::new();
        }
        let mut by_field: HashMap<&str, Vec<&PolicyRow>> = HashMap::new();
        for policy in &self.field_policies {
            if policy.action != "read" {
                continue;
            }
            let Some(field) = &policy.field else { continue };
            by_field.entry(field.as_str()).or_default().push(policy);
        }
        by_field
            .into_iter()
            .filter(|(_, policies)| verdict_is_record_dependent(policies.iter().copied(), context))
            .map(|(field, _)| field.to_string())
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

/// How one policy behaves across *every* record state, for a fixed caller.
enum MatchStability {
    /// Matches whatever the record holds — no condition at all, or a condition on the caller's
    /// own context, whose answer this request has already fixed.
    Always,
    /// Can never match this caller in any record state — their roles fail the policy's gate, or
    /// a context condition this request already fails.
    Never,
    /// Matches or not depending on the record's own values, so the caller can steer it by
    /// editing the record.
    RecordDependent,
}

/// A context-subject condition is **not** record-dependent even though it is a condition: the
/// record is never consulted (`evaluate_policy_row` only passes the record as subject when
/// `subject == "record"`), so the answer is settled by the request. Getting this wrong is not
/// hypothetical — the first probe written for this hazard used a context-subject policy, so its
/// condition was never evaluated against the record and the test passed while proving nothing.
fn match_stability(policy: &PolicyRow, context: &RequestContext) -> MatchStability {
    if !role_gate_passed(policy.roles.as_deref(), context.roles.as_deref()) {
        return MatchStability::Never;
    }
    let Some(condition) = &policy.condition else {
        return MatchStability::Always;
    };
    if policy.subject == "record" {
        return MatchStability::RecordDependent;
    }
    if evaluate_condition(condition, &context.to_value(), context) == ConditionResult::Passed {
        MatchStability::Always
    } else {
        MatchStability::Never
    }
}

/// Whether `policies` can yield different verdicts for different record states — the exact
/// question, not the approximation "does a conditional policy exist".
///
/// Mirrors `evaluate_policies`' own resolution rules, which is why the order below is what it
/// is: a matching `Deny` wins over any number of `Allow`s, and with nothing matching the verdict
/// is `NoMatch` (refused). So the verdict is pinned, and the record cannot move it, when an
/// unconditional `Deny` is present (always refused) or when an unconditional `Allow` is present
/// with no steerable `Deny` to override it (always allowed). An empty set is pinned too, which
/// is what makes the record-level allow-by-default case fall out without a special branch.
fn verdict_is_record_dependent<'a>(
    policies: impl IntoIterator<Item = &'a PolicyRow>,
    context: &RequestContext,
) -> bool {
    let (mut always_allow, mut always_deny) = (false, false);
    let (mut steerable_allow, mut steerable_deny) = (false, false);
    for policy in policies {
        let deny = matches!(policy.effect, PolicyEffect::Deny);
        match match_stability(policy, context) {
            MatchStability::Never => {}
            MatchStability::Always if deny => always_deny = true,
            MatchStability::Always => always_allow = true,
            MatchStability::RecordDependent if deny => steerable_deny = true,
            MatchStability::RecordDependent => steerable_allow = true,
        }
    }

    if always_deny {
        // Refused in every state: nothing the record can say outranks a matching Deny.
        return false;
    }
    if steerable_deny {
        // The record can pull the verdict down to Deny, whatever else grants it.
        return true;
    }
    // Left with Allows only. An unconditional one settles it; otherwise a steerable Allow is the
    // only thing that could grant at all, so the record decides.
    !always_allow && steerable_allow
}

#[cfg(test)]
mod state_dependence_tests {
    use super::*;
    use crate::policy_condition::{ConditionOp, PolicyValue};
    use crate::PolicyCondition;

    fn ctx(roles: &[&str]) -> RequestContext {
        RequestContext {
            tenant_id: Uuid::new_v4().to_string(),
            user_id: Some(Uuid::new_v4().to_string()),
            roles: Some(roles.iter().map(|r| r.to_string()).collect()),
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        }
    }

    fn policy(
        roles: Option<&[&str]>,
        subject: &str,
        condition: Option<PolicyCondition>,
        effect: PolicyEffect,
    ) -> PolicyRow {
        PolicyRow {
            id: Uuid::new_v4(),
            tenant_id: Uuid::new_v4(),
            entity: "test.orders".to_string(),
            action: "read".to_string(),
            field: None,
            subject: subject.to_string(),
            roles: roles.map(|r| r.iter().map(|x| x.to_string()).collect()),
            condition,
            created_by: None,
            effect,
        }
    }

    fn eq(attribute: &str, literal: serde_json::Value) -> PolicyCondition {
        PolicyCondition::Attribute {
            attribute: attribute.to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal { literal },
        }
    }

    /// Record-level allow-by-default: no policies at all means allowed in every state, so there
    /// is nothing for the record to move. Falls out of the same formula, no special branch.
    #[test]
    fn an_empty_policy_set_is_not_record_dependent() {
        assert!(!verdict_is_record_dependent(&[], &ctx(&["viewer"])));
    }

    #[test]
    fn a_record_conditional_allow_alone_is_record_dependent() {
        let policies = vec![policy(
            None,
            "record",
            Some(eq("resolution", serde_json::json!("unlocked"))),
            PolicyEffect::Allow,
        )];
        assert!(verdict_is_record_dependent(&policies, &ctx(&["viewer"])));
    }

    /// The case the earlier approximation got wrong, and the reason this function exists: the
    /// unconditional grant already settles the verdict, so the conditional one alongside it can
    /// only ever be redundant. Masking here withheld history for no reason.
    #[test]
    fn an_unconditional_allow_settles_the_verdict_despite_a_conditional_one() {
        let policies = vec![
            policy(None, "context", None, PolicyEffect::Allow),
            policy(
                None,
                "record",
                Some(eq("resolution", serde_json::json!("unlocked"))),
                PolicyEffect::Allow,
            ),
        ];
        assert!(!verdict_is_record_dependent(&policies, &ctx(&["viewer"])));
    }

    /// ...but a steerable *Deny* is not redundant: `evaluate_policies` lets a matching Deny
    /// outrank every Allow, so the record can still pull the verdict down.
    #[test]
    fn a_conditional_deny_outranks_an_unconditional_allow() {
        let policies = vec![
            policy(None, "context", None, PolicyEffect::Allow),
            policy(
                None,
                "record",
                Some(eq("resolution", serde_json::json!("locked"))),
                PolicyEffect::Deny,
            ),
        ];
        assert!(verdict_is_record_dependent(&policies, &ctx(&["viewer"])));
    }

    /// Refused in every state — nothing the record says outranks a matching Deny, so the caller
    /// cannot steer anything. (They see no history either way; this is about not reporting a
    /// dependence that does not exist.)
    #[test]
    fn an_unconditional_deny_pins_the_verdict() {
        let policies = vec![
            policy(None, "context", None, PolicyEffect::Deny),
            policy(
                None,
                "record",
                Some(eq("resolution", serde_json::json!("unlocked"))),
                PolicyEffect::Allow,
            ),
        ];
        assert!(!verdict_is_record_dependent(&policies, &ctx(&["viewer"])));
    }

    /// A conditional policy aimed at roles this caller does not hold can never match for them,
    /// in any record state.
    #[test]
    fn a_conditional_policy_the_caller_fails_the_role_gate_for_is_inert() {
        let policies = vec![policy(
            Some(&["finance"]),
            "record",
            Some(eq("resolution", serde_json::json!("unlocked"))),
            PolicyEffect::Allow,
        )];
        assert!(!verdict_is_record_dependent(&policies, &ctx(&["viewer"])));
    }

    /// A condition on the caller's own context never consults the record, so the request has
    /// already fixed its answer — conditional, but not steerable. Asserted in both directions so
    /// this cannot pass by treating every context condition as inert.
    #[test]
    fn a_context_subject_condition_is_not_record_dependent_either_way() {
        let granting = vec![policy(
            None,
            "context",
            Some(eq("roles", serde_json::json!(["viewer"]))),
            PolicyEffect::Allow,
        )];
        assert!(!verdict_is_record_dependent(&granting, &ctx(&["viewer"])));

        let non_matching = vec![policy(
            None,
            "context",
            Some(eq("roles", serde_json::json!(["finance"]))),
            PolicyEffect::Allow,
        )];
        assert!(!verdict_is_record_dependent(&non_matching, &ctx(&["viewer"])));
    }
}
