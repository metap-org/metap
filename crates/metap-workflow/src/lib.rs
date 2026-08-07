//! Mirrors `packages/core/src/core/workflow/workflow-engine.ts`. No `WorkflowEngine`
//! struct — the TS class holds no real state either (its one constructor dependency,
//! `OutboxService`, is only ever used to reach `enqueue`, which itself ignores `this` and
//! only uses the `executor` parameter — see `metap-infra`'s `outbox.rs` doc comment). A
//! plain function module is the faithful equivalent, not a stateful wrapper around nothing.

use metap_infra::{enqueue_outbox_event, OutboxEvent};
use metap_metadata::{EntityDefinition, WorkflowTransition};
use metap_permission::{evaluate_condition, RequestContext};
use serde_json::{json, Value};
use sqlx::PgExecutor;
use uuid::Uuid;

pub type JsonObject = serde_json::Map<String, Value>;

/// If the entity has no workflow, there's no initial status. If the caller already
/// supplied a value for the state field, that wins (matches the TS version: an explicit
/// status in `data` isn't overwritten). Otherwise falls back to `workflow.initialState`.
pub fn get_initial_status(entity: &EntityDefinition, data: &JsonObject) -> Option<String> {
    let workflow = entity.workflow.as_ref()?;
    match data.get(&workflow.state_field).and_then(Value::as_str) {
        Some(explicit) => Some(explicit.to_string()),
        None => Some(workflow.initial_state.clone()),
    }
}

pub fn find_transition<'a>(
    entity: &'a EntityDefinition,
    action: &str,
    from_state: &str,
) -> Option<&'a WorkflowTransition> {
    entity
        .workflow
        .as_ref()?
        .transitions
        .iter()
        .find(|t| t.action == action && t.from == from_state)
}

/// `Ok(())` if the guard passes or there is none; `Err(reason)` otherwise — the same
/// `true | string` shape as the TS `runGuard`, just spelled as a `Result` instead of a
/// union. The guard itself is a `PolicyCondition`, not a function — see `entity.rs`'s doc
/// comment on `WorkflowTransition::guard` for why.
pub fn run_guard(
    transition: &WorkflowTransition,
    data: &JsonObject,
    context: &RequestContext,
) -> Result<(), String> {
    let Some(guard) = &transition.guard else { return Ok(()) };
    let subject = Value::Object(data.clone());
    let result = evaluate_condition(guard, &subject, context);
    if result.is_passed() {
        Ok(())
    } else {
        Err(result.reason().unwrap_or("guard failed").to_string())
    }
}

fn parse_context_ids(context: &RequestContext) -> anyhow::Result<(Uuid, Option<Uuid>)> {
    let tenant_id = Uuid::parse_str(&context.tenant_id)?;
    let actor = context.user_id.as_deref().map(Uuid::parse_str).transpose()?;
    Ok((tenant_id, actor))
}

/// Append-only audit log — same "never mutate the past" contract as `workflow_events`'
/// role everywhere else in this project.
pub async fn record_event<'c, E: PgExecutor<'c>>(
    executor: E,
    entity: &EntityDefinition,
    record_id: Uuid,
    action: &str,
    from_state: &str,
    to_state: &str,
    context: &RequestContext,
) -> anyhow::Result<()> {
    let (tenant_id, actor) = parse_context_ids(context)?;
    sqlx::query(
        "INSERT INTO workflow_events (tenant_id, entity, record_id, action, from_state, to_state, actor) \
         VALUES ($1, $2, $3, $4, $5, $6, $7)",
    )
    .bind(tenant_id)
    .bind(&entity.name)
    .bind(record_id)
    .bind(action)
    .bind(from_state)
    .bind(to_state)
    .bind(actor)
    .execute(executor)
    .await?;
    Ok(())
}

pub async fn emit_transitioned<'c, E: PgExecutor<'c>>(
    executor: E,
    entity: &EntityDefinition,
    record_id: Uuid,
    action: &str,
    from_state: &str,
    to_state: &str,
    actor: Option<Uuid>,
) -> anyhow::Result<()> {
    let event = OutboxEvent {
        topic: format!("{}.workflow.transitioned", entity.name),
        aggregate_type: entity.name.clone(),
        aggregate_id: record_id,
        payload: json!({
            "recordId": record_id,
            "action": action,
            "from": from_state,
            "to": to_state,
            "actor": actor,
        }),
    };
    enqueue_outbox_event(executor, &event).await
}

pub async fn emit_created<'c, E: PgExecutor<'c>>(
    executor: E,
    entity: &EntityDefinition,
    record_id: Uuid,
    data: &JsonObject,
) -> anyhow::Result<()> {
    let event = OutboxEvent {
        topic: format!("{}.record.created", entity.name),
        aggregate_type: entity.name.clone(),
        aggregate_id: record_id,
        payload: json!({ "recordId": record_id, "data": data }),
    };
    enqueue_outbox_event(executor, &event).await
}

pub async fn emit_deleted<'c, E: PgExecutor<'c>>(
    executor: E,
    entity: &EntityDefinition,
    record_id: Uuid,
) -> anyhow::Result<()> {
    let event = OutboxEvent {
        topic: format!("{}.record.deleted", entity.name),
        aggregate_type: entity.name.clone(),
        aggregate_id: record_id,
        payload: json!({ "recordId": record_id }),
    };
    enqueue_outbox_event(executor, &event).await
}

pub async fn emit_updated<'c, E: PgExecutor<'c>>(
    executor: E,
    entity: &EntityDefinition,
    record_id: Uuid,
    data: &JsonObject,
    version: i32,
) -> anyhow::Result<()> {
    let event = OutboxEvent {
        topic: format!("{}.record.updated", entity.name),
        aggregate_type: entity.name.clone(),
        aggregate_id: record_id,
        payload: json!({ "recordId": record_id, "data": data, "version": version }),
    };
    enqueue_outbox_event(executor, &event).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use metap_metadata::{EntityWorkflow, FieldKind};
    use metap_permission::{ConditionOp, PolicyCondition, PolicyValue};

    fn entity_with_workflow() -> EntityDefinition {
        EntityDefinition {
            name: "crm.customers".to_string(),
            label: "Customer".to_string(),
            table_name: "records".to_string(),
            fields: vec![metap_metadata::EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: None,
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "active".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: None,
            }],
            list_views: vec![],
            workflow: Some(EntityWorkflow {
                state_field: "status".to_string(),
                initial_state: "draft".to_string(),
                terminal_states: vec!["active".to_string()],
                transitions: vec![WorkflowTransition {
                    action: "activate".to_string(),
                    from: "draft".to_string(),
                    to: "active".to_string(),
                    label: "Activate".to_string(),
                    guard: None,
                }],
            }),
        }
    }

    fn context(user_id: Option<&str>) -> RequestContext {
        RequestContext {
            tenant_id: Uuid::new_v4().to_string(),
            user_id: user_id.map(String::from),
            roles: None,
            function_id: None,
        }
    }

    #[test]
    fn initial_status_defaults_to_workflow_initial_state() {
        let entity = entity_with_workflow();
        let data = JsonObject::new();
        assert_eq!(get_initial_status(&entity, &data), Some("draft".to_string()));
    }

    #[test]
    fn initial_status_respects_explicit_value_in_data() {
        let entity = entity_with_workflow();
        let mut data = JsonObject::new();
        data.insert("status".to_string(), json!("active"));
        assert_eq!(get_initial_status(&entity, &data), Some("active".to_string()));
    }

    #[test]
    fn initial_status_is_none_without_a_workflow() {
        let mut entity = entity_with_workflow();
        entity.workflow = None;
        assert_eq!(get_initial_status(&entity, &JsonObject::new()), None);
    }

    #[test]
    fn find_transition_matches_on_action_and_from_state() {
        let entity = entity_with_workflow();
        assert!(find_transition(&entity, "activate", "draft").is_some());
        assert!(find_transition(&entity, "activate", "active").is_none());
        assert!(find_transition(&entity, "nope", "draft").is_none());
    }

    #[test]
    fn guard_less_transition_always_passes() {
        let entity = entity_with_workflow();
        let transition = find_transition(&entity, "activate", "draft").unwrap();
        let ctx = context(None);
        assert!(run_guard(transition, &JsonObject::new(), &ctx).is_ok());
    }

    #[test]
    fn guarded_transition_evaluates_the_policy_condition_against_data() {
        let mut transition = WorkflowTransition {
            action: "approve".to_string(),
            from: "draft".to_string(),
            to: "approved".to_string(),
            label: "Approve".to_string(),
            guard: Some(PolicyCondition::Attribute {
                attribute: "amount".to_string(),
                op: ConditionOp::Eq,
                value: PolicyValue::Literal { literal: json!(100) },
            }),
        };
        let ctx = context(None);

        let mut data = JsonObject::new();
        data.insert("amount".to_string(), json!(100));
        assert!(run_guard(&transition, &data, &ctx).is_ok());

        data.insert("amount".to_string(), json!(50));
        let err = run_guard(&transition, &data, &ctx).unwrap_err();
        assert!(err.contains("condition failed"));

        transition.guard = None; // sanity: guard-less path still passes regardless of data
        assert!(run_guard(&transition, &data, &ctx).is_ok());
    }
}
