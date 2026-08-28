use metap_permission::{EntityAction, RequestContext};
use metap_query::{
    apply_params, encode_cursor, plan_list, CrossRecordConditionInListError, Cursor, InvalidCursorError,
    InvalidJqlError, ListInput, SortDir, UnknownListViewError,
};

use crate::dto::RecordDto;
use crate::result::{PageInfo, ServiceResult};

use super::helpers::{
    forbidden, is_dedicated, mask_record_for_read, router_unavailable, row_to_dto, row_to_dto_dedicated,
    sort_field_value, RECORD_COLUMNS, RECORD_COLUMNS_DEDICATED,
};
use super::CrudService;

impl CrudService {
    pub async fn list(
        &self,
        entity_name: &str,
        input: &ListInput,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        // Loaded once, up front, and reused for both `entity` and `plan_list` below —
        // loading it twice (once via `get_entity`, again here) would let a publish/rollback
        // land in between and tear this request between two registry versions (see the
        // struct doc comment's "one snapshot per call" invariant).
        let metadata = self.metadata.load();
        let Some(entity) = metadata.get_entity(entity_name).cloned() else {
            tracing::debug!(entity = entity_name, "list rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_policies = snapshot.get_record_policies(EntityAction::Read);

        let planned = match plan_list(
            &metadata,
            &self.permissions,
            &entity.name,
            input,
            context,
            record_policies,
        ) {
            Ok(p) => p,
            Err(e) => {
                if e.downcast_ref::<InvalidCursorError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "invalid_cursor", e.to_string()));
                }
                if e.downcast_ref::<UnknownListViewError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "unknown_list_view", e.to_string()));
                }
                if e.downcast_ref::<InvalidJqlError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "invalid_jql", e.to_string()));
                }
                // Deterministic/permanent (an entity read-policy misconfiguration, not
                // something this specific request can fix) — still a `5xx`, but with its own
                // `code` and message rather than falling through to a generic, indistinguishable
                // `internal_error` (found in code review, 2026-08-22).
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

        let dedicated = is_dedicated(&entity);
        let table = &entity.table_name;
        let columns = if dedicated {
            RECORD_COLUMNS_DEDICATED
        } else {
            RECORD_COLUMNS
        };
        let sql = format!(
            "SELECT {columns} FROM {table} WHERE {} ORDER BY {} LIMIT {}",
            planned.where_sql,
            planned.order_by_sql,
            planned.limit + 1
        );
        let mut tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };
        let query = apply_params(sqlx::query(&sql), &planned.params);
        let rows = query.fetch_all(&mut *tx).await?;

        let has_more = rows.len() as i64 > planned.limit;
        let page_rows: Vec<_> = if has_more {
            rows.into_iter().take(planned.limit as usize).collect()
        } else {
            rows
        };
        let page_dtos: Vec<RecordDto> = page_rows
            .into_iter()
            .map(|row| {
                if dedicated {
                    row_to_dto_dedicated(row, &entity.name)
                } else {
                    row_to_dto(row)
                }
            })
            .collect::<anyhow::Result<_>>()?;

        let next_cursor = if has_more {
            page_dtos.last().map(|last| {
                encode_cursor(&Cursor {
                    field: planned.resolved_sort.field.clone(),
                    value: sort_field_value(last, &planned.resolved_sort.field),
                    id: last.id.to_string(),
                    dir: if planned.resolved_sort.descending {
                        SortDir::Desc
                    } else {
                        SortDir::Asc
                    },
                })
            })
        } else {
            None
        };

        let data: Vec<RecordDto> = page_dtos
            .into_iter()
            .map(|dto| mask_record_for_read(&entity, context, &snapshot, dto))
            .collect();
        let data = self
            .hydrate_related_display(&entity, context, tenant_id, &mut tx, data)
            .await?;
        tx.commit().await?;

        Ok(ServiceResult::ok_with_page(
            data,
            PageInfo {
                limit: planned.limit,
                next_cursor,
            },
        ))
    }
}
