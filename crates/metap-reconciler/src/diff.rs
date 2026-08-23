//! `diff(desired, actual, renames) -> Vec<DdlOp>` — §5.4's algorithm. Compares two
//! `PhysicalSchema` values (never an `EntityDefinition` against raw `pg_catalog`), normalizing
//! expressions at comparison time (`normalize::normalize_expr`) so Postgres's own
//! canonicalization of what was typed never false-positives a rebuild.

use crate::normalize::normalize_expr;
use crate::schema::{ColumnOrigin, ColumnSpec, Cost, ExecutionMode, FkSpec, IndexSpec, PhysicalSchema, UniqueSpec};

#[derive(Debug, Clone, PartialEq)]
pub enum DdlOp {
    CreateTable,
    AddColumn {
        name: String,
        spec: ColumnSpec,
    },
    RenameColumn {
        from: String,
        to: String,
    },
    /// (Re)creates the `BEFORE INSERT OR UPDATE` trigger that keeps a `storage: column` field's
    /// real column in sync with `data ->> field` — the permanent stand-in for Postgres's
    /// `GENERATED ... STORED`, which this crate never uses (§5.6: avoids the `ACCESS EXCLUSIVE`
    /// table rewrite). Idempotent (`CREATE OR REPLACE TRIGGER`) — safe to re-run on resume.
    AddSyncTrigger {
        field: String,
        sql_type: String,
    },
    /// Checkpointed, resumable backfill of a newly (or not-yet-fully) populated real column
    /// (§5.7). `op_id` keys `reconciler_backfill_progress`.
    BackfillColumn {
        op_id: String,
        column: String,
        source_field: String,
        sql_type: String,
    },
    CreateIndexConcurrently {
        name: String,
        spec: IndexSpec,
    },
    DropIndexConcurrently {
        name: String,
    },
    AddForeignKeyNotValid {
        name: String,
        spec: FkSpec,
    },
    ValidateForeignKey {
        name: String,
    },
    DropForeignKey {
        name: String,
    },
    AddUnique {
        name: String,
        spec: UniqueSpec,
    },
    DropUnique {
        name: String,
    },
}

impl DdlOp {
    /// §5.5 — governs whether the executor must set `Migrating` before running this op.
    pub fn cost(&self) -> Cost {
        match self {
            DdlOp::BackfillColumn { .. } => Cost::Heavy,
            DdlOp::CreateIndexConcurrently { .. }
            | DdlOp::DropIndexConcurrently { .. }
            | DdlOp::ValidateForeignKey { .. } => Cost::BackgroundFast,
            _ => Cost::Instant,
        }
    }

    /// §5.6 — governs how the executor runs this op.
    pub fn execution_mode(&self) -> ExecutionMode {
        match self {
            DdlOp::CreateIndexConcurrently { .. } | DdlOp::DropIndexConcurrently { .. } => {
                ExecutionMode::NonTransactional
            }
            DdlOp::BackfillColumn { .. } => ExecutionMode::Batched,
            _ => ExecutionMode::Transactional,
        }
    }
}

/// §5.1's `reconcile = introspect(actual) → diff → plan`. `actual: None` means the table
/// doesn't exist yet — step 0: emit `CreateTable`, then diff the rest against an empty schema
/// (so every desired column/index/FK gets its normal `Add*` op, not a special "first time"
/// path). `renames` is caller-supplied (there is no way to *infer* a rename from two column
/// sets alone — `old_name` disappearing and `new_name` appearing looks identical to a drop +
/// an unrelated add) and is applied first so every step after it never sees the stale name.
pub fn diff(desired: &PhysicalSchema, actual: Option<&PhysicalSchema>, renames: &[(String, String)]) -> Vec<DdlOp> {
    let mut ops = Vec::new();
    let mut actual = match actual {
        None => {
            ops.push(DdlOp::CreateTable);
            PhysicalSchema::empty(desired.table.clone())
        }
        Some(a) => a.clone(),
    };

    // Step 1: renames, applied to a mutable copy of `actual` so every later step already sees
    // the new name (§5.4: "sửa bản sao actual → bước sau không thấy old→new là drop+add").
    for (from, to) in renames {
        if actual.columns.contains_key(to) || !actual.columns.contains_key(from) || !desired.columns.contains_key(to) {
            continue;
        }
        let spec = actual.columns.remove(from).expect("checked contains_key above");
        actual.columns.insert(to.clone(), spec);
        ops.push(DdlOp::RenameColumn {
            from: from.clone(),
            to: to.clone(),
        });
    }

    // Step 2: columns — add missing, never drop (no prune in step 2's scope: §5.4 "thừa→giữ
    // (chỉ drop nếu bật prune)"; a framework column is never a candidate anyway). A `Generated`
    // column that exists but isn't fully backfilled yet (crash-resume) gets its trigger
    // re-asserted and its backfill re-queued — free resume, no "already added" duplicate.
    for (name, spec) in &desired.columns {
        match actual.columns.get(name) {
            None => {
                ops.push(DdlOp::AddColumn {
                    name: name.clone(),
                    spec: spec.clone(),
                });
                if let ColumnOrigin::Generated { source_field, .. } = &spec.origin {
                    push_sync_and_backfill(&mut ops, &desired.table, name, source_field, &spec.sql_type);
                }
            }
            Some(actual_spec) => {
                if let ColumnOrigin::Generated {
                    source_field,
                    backfilled: false,
                } = &actual_spec.origin
                {
                    push_sync_and_backfill(&mut ops, &desired.table, name, source_field, &spec.sql_type);
                }
            }
        }
    }

    // Step 3: indexes.
    for (name, spec) in &desired.indexes {
        match actual.indexes.get(name) {
            None => ops.push(DdlOp::CreateIndexConcurrently {
                name: name.clone(),
                spec: spec.clone(),
            }),
            Some(actual_idx) => {
                let rebuild = !actual_idx.valid || !index_matches(actual_idx, spec);
                if rebuild {
                    ops.push(DdlOp::DropIndexConcurrently { name: name.clone() });
                    ops.push(DdlOp::CreateIndexConcurrently {
                        name: name.clone(),
                        spec: spec.clone(),
                    });
                }
            }
        }
    }
    for name in actual.indexes.keys() {
        if !desired.indexes.contains_key(name) {
            ops.push(DdlOp::DropIndexConcurrently { name: name.clone() });
        }
    }

    // Step 4: foreign keys. A brand-new FK gets *both* `AddForeignKeyNotValid` and
    // `ValidateForeignKey` in this same pass (nothing stops them running back to back) — the
    // "resume, only VALIDATE" rule (§5.4) is specifically for the crash-recovery case where a
    // *previous* pass's `AddForeignKeyNotValid` already committed but its `ValidateForeignKey`
    // never ran (the `Some(actual_fk)` arm below), not a reason to split a fresh add across two
    // separate `reconcile()` calls.
    for (name, spec) in &desired.foreign_keys {
        match actual.foreign_keys.get(name) {
            None => {
                ops.push(DdlOp::AddForeignKeyNotValid {
                    name: name.clone(),
                    spec: spec.clone(),
                });
                ops.push(DdlOp::ValidateForeignKey { name: name.clone() });
            }
            Some(actual_fk) => {
                if !fk_matches(actual_fk, spec) {
                    ops.push(DdlOp::DropForeignKey { name: name.clone() });
                    ops.push(DdlOp::AddForeignKeyNotValid {
                        name: name.clone(),
                        spec: spec.clone(),
                    });
                    ops.push(DdlOp::ValidateForeignKey { name: name.clone() });
                } else if !actual_fk.validated {
                    // §5.4: "crash giữa NOT VALID và VALIDATE → chỉ VALIDATE, không add trùng".
                    ops.push(DdlOp::ValidateForeignKey { name: name.clone() });
                }
            }
        }
    }
    for name in actual.foreign_keys.keys() {
        if !desired.foreign_keys.contains_key(name) {
            ops.push(DdlOp::DropForeignKey { name: name.clone() });
        }
    }

    // Step 5: unique constraints (not the unique *indexes* created above for a
    // `storage: column` field — those already enforce uniqueness on their own; this is the
    // separately-named constraint `compile()` also emits for that case).
    for (name, spec) in &desired.uniques {
        match actual.uniques.get(name) {
            None => ops.push(DdlOp::AddUnique {
                name: name.clone(),
                spec: spec.clone(),
            }),
            Some(actual_uq) if actual_uq.columns != spec.columns => {
                ops.push(DdlOp::DropUnique { name: name.clone() });
                ops.push(DdlOp::AddUnique {
                    name: name.clone(),
                    spec: spec.clone(),
                });
            }
            Some(_) => {}
        }
    }
    for name in actual.uniques.keys() {
        if !desired.uniques.contains_key(name) {
            ops.push(DdlOp::DropUnique { name: name.clone() });
        }
    }

    topo_sort(ops)
}

fn push_sync_and_backfill(ops: &mut Vec<DdlOp>, table: &str, column: &str, source_field: &str, sql_type: &str) {
    ops.push(DdlOp::AddSyncTrigger {
        field: source_field.to_string(),
        sql_type: sql_type.to_string(),
    });
    ops.push(DdlOp::BackfillColumn {
        op_id: crate::introspect::backfill_op_id(table, column),
        column: column.to_string(),
        source_field: source_field.to_string(),
        sql_type: sql_type.to_string(),
    });
}

fn index_matches(actual: &IndexSpec, desired: &IndexSpec) -> bool {
    normalize_expr(&actual.expression) == normalize_expr(&desired.expression)
        && actual.unique == desired.unique
        && actual.using == desired.using
}

fn fk_matches(actual: &FkSpec, desired: &FkSpec) -> bool {
    actual.column == desired.column
        && actual.ref_table == desired.ref_table
        && actual.ref_column == desired.ref_column
        && actual.on_delete == desired.on_delete
}

/// §5.4 step 5: `CreateTable → AddColumn → MigrateData → Index → FK NotValid → ValidateFK →
/// Drop`. A stable sort by rank, with a same-name rebuild's `Drop` given the *same* rank as its
/// paired `Create`/`Add` (rather than a later "drop" bucket) — `diff()` above always pushes the
/// drop immediately before its rebuild partner, and a stable sort preserves that relative order,
/// so "drop this index, then recreate it" can never land the recreate before its own drop.
fn topo_sort(mut ops: Vec<DdlOp>) -> Vec<DdlOp> {
    fn rank(op: &DdlOp) -> u8 {
        match op {
            DdlOp::CreateTable => 0,
            DdlOp::RenameColumn { .. } => 1,
            DdlOp::AddColumn { .. } => 2,
            DdlOp::AddSyncTrigger { .. } => 3,
            DdlOp::BackfillColumn { .. } => 4,
            DdlOp::CreateIndexConcurrently { .. } | DdlOp::DropIndexConcurrently { .. } => 5,
            DdlOp::AddUnique { .. } | DdlOp::DropUnique { .. } => 6,
            DdlOp::AddForeignKeyNotValid { .. } | DdlOp::DropForeignKey { .. } => 7,
            DdlOp::ValidateForeignKey { .. } => 8,
        }
    }
    ops.sort_by_key(rank);
    ops
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::schema::OnDelete;

    fn col(sql_type: &str) -> ColumnSpec {
        ColumnSpec {
            sql_type: sql_type.to_string(),
            nullable: true,
            origin: ColumnOrigin::Framework,
        }
    }

    fn idx(expression: &str) -> IndexSpec {
        IndexSpec {
            expression: expression.to_string(),
            unique: false,
            using: None,
            valid: true,
        }
    }

    #[test]
    fn no_actual_table_creates_table_and_every_desired_piece() {
        let mut desired = PhysicalSchema::empty("hr_employees");
        desired.columns.insert("id".to_string(), col("uuid"));
        desired
            .indexes
            .insert("idx_hr_employees_dept".to_string(), idx("(data ->> 'dept')::text"));

        let ops = diff(&desired, None, &[]);
        assert_eq!(ops[0], DdlOp::CreateTable);
        assert!(ops
            .iter()
            .any(|o| matches!(o, DdlOp::AddColumn { name, .. } if name == "id")));
        assert!(ops
            .iter()
            .any(|o| matches!(o, DdlOp::CreateIndexConcurrently { name, .. } if name == "idx_hr_employees_dept")));
    }

    #[test]
    fn matching_actual_produces_no_ops() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert("id".to_string(), col("uuid"));
        desired
            .indexes
            .insert("idx_t_f".to_string(), idx("(data ->> 'f')::text"));
        let actual = desired.clone();

        assert!(diff(&desired, Some(&actual), &[]).is_empty());
    }

    #[test]
    fn matching_after_normalization_produces_no_ops_even_when_raw_strings_differ() {
        let mut desired = PhysicalSchema::empty("t");
        desired
            .indexes
            .insert("idx_t_f".to_string(), idx("(data ->> 'f')::text"));
        let mut actual = PhysicalSchema::empty("t");
        // What Postgres would echo back for the same expression (probed live, see normalize.rs).
        actual
            .indexes
            .insert("idx_t_f".to_string(), idx("((data ->> 'f'::text))"));

        assert!(diff(&desired, Some(&actual), &[]).is_empty());
    }

    #[test]
    fn invalid_index_is_dropped_then_recreated_drop_strictly_before_create() {
        let mut desired = PhysicalSchema::empty("t");
        desired
            .indexes
            .insert("idx_t_f".to_string(), idx("(data ->> 'f')::text"));
        let mut actual = desired.clone();
        actual.indexes.get_mut("idx_t_f").unwrap().valid = false;

        let ops = diff(&desired, Some(&actual), &[]);
        let drop_pos = ops
            .iter()
            .position(|o| matches!(o, DdlOp::DropIndexConcurrently { .. }))
            .unwrap();
        let create_pos = ops
            .iter()
            .position(|o| matches!(o, DdlOp::CreateIndexConcurrently { .. }))
            .unwrap();
        assert!(drop_pos < create_pos);
    }

    #[test]
    fn missing_column_not_in_desired_is_never_dropped() {
        let desired = PhysicalSchema::empty("t");
        let mut actual = PhysicalSchema::empty("t");
        actual.columns.insert("stale".to_string(), col("text"));

        assert!(diff(&desired, Some(&actual), &[]).is_empty());
    }

    #[test]
    fn rename_applies_before_column_diff_so_no_drop_add_pair_appears() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert("new_name".to_string(), col("text"));
        let mut actual = PhysicalSchema::empty("t");
        actual.columns.insert("old_name".to_string(), col("text"));

        let ops = diff(
            &desired,
            Some(&actual),
            &[("old_name".to_string(), "new_name".to_string())],
        );
        assert_eq!(
            ops,
            vec![DdlOp::RenameColumn {
                from: "old_name".to_string(),
                to: "new_name".to_string()
            }]
        );
    }

    #[test]
    fn unbackfilled_generated_column_that_already_exists_resumes_without_readd() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert(
            "amount".to_string(),
            ColumnSpec {
                sql_type: "numeric(18,4)".to_string(),
                nullable: true,
                origin: ColumnOrigin::Generated {
                    source_field: "amount".to_string(),
                    backfilled: true,
                },
            },
        );
        let mut actual = PhysicalSchema::empty("t");
        actual.columns.insert(
            "amount".to_string(),
            ColumnSpec {
                sql_type: "numeric(18,4)".to_string(),
                nullable: true,
                origin: ColumnOrigin::Generated {
                    source_field: "amount".to_string(),
                    backfilled: false,
                },
            },
        );

        let ops = diff(&desired, Some(&actual), &[]);
        assert!(
            !ops.iter().any(|o| matches!(o, DdlOp::AddColumn { .. })),
            "must not re-add an existing column"
        );
        assert!(
            ops.iter().any(|o| matches!(o, DdlOp::BackfillColumn { .. })),
            "must resume the backfill"
        );
    }

    #[test]
    fn backfilled_generated_column_produces_no_ops() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert(
            "amount".to_string(),
            ColumnSpec {
                sql_type: "numeric(18,4)".to_string(),
                nullable: true,
                origin: ColumnOrigin::Generated {
                    source_field: "amount".to_string(),
                    backfilled: true,
                },
            },
        );
        let actual = desired.clone();
        assert!(diff(&desired, Some(&actual), &[]).is_empty());
    }

    #[test]
    fn fk_added_not_valid_then_validated_when_missing() {
        let mut desired = PhysicalSchema::empty("hr_employees");
        desired.foreign_keys.insert(
            "fk_hr_employees_departmentid".to_string(),
            FkSpec {
                column: "departmentId".to_string(),
                ref_table: "hr_departments".to_string(),
                ref_column: "id".to_string(),
                on_delete: OnDelete::Restrict,
                validated: true,
            },
        );

        let ops = diff(&desired, None, &[]);
        let add_pos = ops
            .iter()
            .position(|o| matches!(o, DdlOp::AddForeignKeyNotValid { .. }))
            .unwrap();
        let validate_pos = ops
            .iter()
            .position(|o| matches!(o, DdlOp::ValidateForeignKey { .. }))
            .unwrap();
        assert!(
            add_pos < validate_pos,
            "a brand-new FK must be added then validated in the same pass"
        );
    }

    #[test]
    fn fk_added_but_not_validated_only_validates_no_duplicate_add() {
        let mut desired = PhysicalSchema::empty("t");
        desired.foreign_keys.insert(
            "fk_t_x".to_string(),
            FkSpec {
                column: "x".to_string(),
                ref_table: "other".to_string(),
                ref_column: "id".to_string(),
                on_delete: OnDelete::Restrict,
                validated: true,
            },
        );
        let mut actual = desired.clone();
        actual.foreign_keys.get_mut("fk_t_x").unwrap().validated = false;

        let ops = diff(&desired, Some(&actual), &[]);
        assert_eq!(
            ops,
            vec![DdlOp::ValidateForeignKey {
                name: "fk_t_x".to_string()
            }]
        );
    }

    #[test]
    fn orphan_index_and_fk_and_unique_are_dropped() {
        let desired = PhysicalSchema::empty("t");
        let mut actual = PhysicalSchema::empty("t");
        actual.indexes.insert("idx_gone".to_string(), idx("x"));
        actual.foreign_keys.insert(
            "fk_gone".to_string(),
            FkSpec {
                column: "x".to_string(),
                ref_table: "other".to_string(),
                ref_column: "id".to_string(),
                on_delete: OnDelete::Restrict,
                validated: true,
            },
        );
        actual.uniques.insert(
            "uq_gone".to_string(),
            UniqueSpec {
                columns: vec!["x".to_string()],
            },
        );

        let ops = diff(&desired, Some(&actual), &[]);
        assert!(ops.contains(&DdlOp::DropIndexConcurrently {
            name: "idx_gone".to_string()
        }));
        assert!(ops.contains(&DdlOp::DropForeignKey {
            name: "fk_gone".to_string()
        }));
        assert!(ops.contains(&DdlOp::DropUnique {
            name: "uq_gone".to_string()
        }));
    }

    #[test]
    fn topo_order_create_table_before_columns_before_indexes_before_fks() {
        let mut desired = PhysicalSchema::empty("t");
        desired.columns.insert("id".to_string(), col("uuid"));
        desired.indexes.insert("idx_t_f".to_string(), idx("f"));
        desired.foreign_keys.insert(
            "fk_t_f".to_string(),
            FkSpec {
                column: "f".to_string(),
                ref_table: "other".to_string(),
                ref_column: "id".to_string(),
                on_delete: OnDelete::Restrict,
                validated: true,
            },
        );

        let ops = diff(&desired, None, &[]);
        let pos = |pred: &dyn Fn(&DdlOp) -> bool| ops.iter().position(pred).unwrap();
        let p_table = pos(&|o| matches!(o, DdlOp::CreateTable));
        let p_col = pos(&|o| matches!(o, DdlOp::AddColumn { .. }));
        let p_idx = pos(&|o| matches!(o, DdlOp::CreateIndexConcurrently { .. }));
        let p_fk = pos(&|o| matches!(o, DdlOp::AddForeignKeyNotValid { .. }));
        assert!(p_table < p_col);
        assert!(p_col < p_idx);
        assert!(p_idx < p_fk);
    }
}
