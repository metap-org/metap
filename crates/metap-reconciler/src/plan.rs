//! A pure, DB-free "what would `reconcile()` run" preview — `plan(desired, actual, renames)`
//! calls the same `diff()` the real apply path uses and renders each resulting `DdlOp` to the
//! exact SQL `executor::execute()` would run, without ever opening a connection. Two real
//! consumers: a regression test can assert on the final SQL a transition produces without a live
//! Postgres (see `diff/tests.rs`'s `unique_constraint_to_partial_index_same_name_...` test), and
//! `dev-tools reconcile-plan` lets a human review generated DDL before applying it by hand —
//! the same split modern migration tools make (e.g. Prisma's `migrate dev` vs `migrate diff
//! --script`) — as an addition alongside, never a replacement for, the auto-apply `reconcile()`
//! every downstream binary's boot sequence already relies on.

use crate::diff::{diff, DdlOp};
use crate::executor::build_sql;
use crate::schema::PhysicalSchema;

/// One planned DDL operation and the literal SQL `execute()` would run for it. `sql` is empty
/// for `DdlOp::BackfillColumn` — that op is a checkpointed batch loop
/// (`backfill::run_batched_update`), not a single SQL statement, so there is nothing meaningful
/// to print; a caller rendering a plan for a human should say so explicitly instead of showing
/// nothing.
#[derive(Debug, Clone)]
pub struct PlannedOp {
    pub op: DdlOp,
    pub sql: Vec<String>,
}

/// Same signature as `diff::diff`, plus the table name to render SQL against (`diff()` itself
/// takes that from `desired.table`, so `plan()` just reads it back out rather than asking the
/// caller to repeat it).
pub fn plan(desired: &PhysicalSchema, actual: Option<&PhysicalSchema>, renames: &[(String, String)]) -> Vec<PlannedOp> {
    diff(desired, actual, renames)
        .into_iter()
        .map(|op| {
            let sql = build_sql(&desired.table, &op);
            PlannedOp { op, sql }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::schema::{ColumnOrigin, ColumnSpec};

    #[test]
    fn plan_renders_sql_for_a_fresh_table_without_touching_a_database() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert(
            "field".to_string(),
            ColumnSpec {
                sql_type: "text".to_string(),
                nullable: true,
                origin: ColumnOrigin::Framework,
            },
        );

        let planned = plan(&desired, None, &[]);

        assert!(matches!(planned[0].op, DdlOp::CreateTable));
        assert!(planned[0].sql[0].starts_with("CREATE TABLE IF NOT EXISTS"));
    }

    #[test]
    fn plan_leaves_backfill_sql_empty_rather_than_fabricating_a_statement() {
        let planned_op = PlannedOp {
            op: DdlOp::BackfillColumn {
                op_id: "op".to_string(),
                column: "c".to_string(),
                source_field: "f".to_string(),
                sql_type: "text".to_string(),
            },
            sql: build_sql(
                "t",
                &DdlOp::BackfillColumn {
                    op_id: "op".to_string(),
                    column: "c".to_string(),
                    source_field: "f".to_string(),
                    sql_type: "text".to_string(),
                },
            ),
        };
        assert!(planned_op.sql.is_empty());
    }
}
