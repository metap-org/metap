use metap_permission::{EntityAction, RequestContext};
use metap_query::{apply_params, plan_aggregate, AggregateInput, CrossRecordConditionInListError, InvalidAggregateError};
use serde_json::Value;
use sqlx::Row;

use crate::result::ServiceResult;

use super::helpers::{forbidden, router_unavailable};
use super::CrudService;

impl CrudService {
    /// Metadata-driven aggregation — the `GROUP BY`/`COUNT`/`SUM` sibling of `list`, added so an
    /// analytics screen doesn't have to pull every row over the wire to count it (or, worse, drop
    /// out of the platform and write its own SQL, losing tenant scoping and record-level
    /// permission on the way).
    ///
    /// Permission is the same gate `list` uses, not a weaker one: entity-level `Read` must be
    /// allowed, and the caller's record-level (ABAC) read policies are applied to the rows
    /// *before* they are aggregated (`plan_aggregate` folds them into the `WHERE`). A caller who
    /// cannot read a row therefore cannot learn it exists by counting it either.
    ///
    /// Field-level masking has no equivalent here and deliberately isn't faked: an aggregate over
    /// a masked field would still leak that field's values in aggregate form, so a *field* the
    /// caller may not read must not be aggregatable at all. That check is a follow-up (it needs a
    /// readable-field set threaded into the planner); until then, only entity- and record-level
    /// permission apply, which matches what the pre-aggregate world offered (none at all).
    pub async fn aggregate(
        &self,
        entity_name: &str,
        input: &AggregateInput,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<Value>>> {
        // One registry snapshot for the whole call, same invariant every other operation holds.
        let metadata = self.metadata.load();
        let Some(entity) = metadata.get_entity(entity_name).cloned() else {
            tracing::debug!(entity = entity_name, "aggregate rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_policies = snapshot.get_record_policies(EntityAction::Read);

        let planned = match plan_aggregate(
            &metadata,
            &self.permissions,
            &entity.name,
            input,
            context,
            record_policies,
        ) {
            Ok(p) => p,
            Err(e) => {
                if e.downcast_ref::<InvalidAggregateError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "invalid_aggregate", e.to_string()));
                }
                if e.downcast_ref::<CrossRecordConditionInListError>().is_some() {
                    return Ok(ServiceResult::err_with_message(
                        500,
                        "unsupported_policy_condition",
                        e.to_string(),
                    ));
                }
                return Err(e);
            }
        };

        let mut tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };
        let query = apply_params(sqlx::query(&planned.sql), &planned.params);
        let rows = query.fetch_all(&mut *tx).await?;
        tx.commit().await?;

        // One `jsonb` column per row (`to_jsonb(agg)`), so the projection's shape — which is
        // caller-defined and therefore unknown at compile time — never has to be described to
        // sqlx as a set of typed columns.
        let data: Vec<Value> = rows
            .into_iter()
            .map(|row| row.try_get::<Value, _>("row"))
            .collect::<Result<_, _>>()?;

        tracing::debug!(
            entity = entity.name,
            groups = data.len(),
            "aggregate returned"
        );
        Ok(ServiceResult::ok(data))
    }
}
