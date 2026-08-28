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
mod tests;
