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
        where_clause: None,
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
fn unique_constraint_to_partial_index_same_name_drops_constraint_before_rebuilding_index() {
    // Reproduces the real, twice-hit "transition-state" bug (WAF's `waf.ddos_policies.zoneId`,
    // Phase 80; Jira's `jira.projects.key`, Phase 82): a `unique: true` field used to be a
    // blanket `UNIQUE` constraint — introspected as *both* an `actual.uniques` entry (the
    // constraint) and an `actual.indexes` entry under the same deterministic name (the
    // constraint's own backing index, which `pg_indexes` lists like any other index) — and
    // `compile()` now wants it to be a plain partial unique index under that same name, with no
    // `UniqueSpec` at all. Before the `topo_sort` rank fix, `DropIndexConcurrently` for this name
    // (from the index-rebuild branch) could run before `DropUnique` (from the separate
    // no-longer-desired-unique branch), and Postgres refuses to drop a constraint's backing index
    // while the constraint still exists — so the rebuild silently never completed.
    let name = "uniq_t_field";
    let mut desired = PhysicalSchema::empty("t");
    desired.indexes.insert(
        name.to_string(),
        IndexSpec {
            expression: "field".to_string(),
            unique: true,
            using: None,
            valid: true,
            where_clause: Some("deleted = false".to_string()),
        },
    );

    let mut actual = PhysicalSchema::empty("t");
    actual.indexes.insert(
        name.to_string(),
        IndexSpec {
            expression: "field".to_string(),
            unique: true,
            using: None,
            valid: true,
            where_clause: None, // the old blanket shape — no partial predicate
        },
    );
    actual.uniques.insert(
        name.to_string(),
        UniqueSpec {
            columns: vec!["field".to_string()],
        },
    );

    let ops = diff(&desired, Some(&actual), &[]);

    let pos = |pred: &dyn Fn(&DdlOp) -> bool| ops.iter().position(pred).unwrap();
    let p_drop_unique = pos(&|o| matches!(o, DdlOp::DropUnique { name: n } if n == name));
    let p_drop_index = pos(&|o| matches!(o, DdlOp::DropIndexConcurrently { name: n } if n == name));
    let p_create_index = pos(&|o| matches!(o, DdlOp::CreateIndexConcurrently { name: n, .. } if n == name));

    assert!(
        p_drop_unique < p_drop_index,
        "DropUnique must run before DropIndexConcurrently for the same name, so the index is \
         already free of its constraint by the time the bare DROP INDEX runs"
    );
    assert!(
        p_drop_unique < p_create_index,
        "DropUnique must run before the new index is created under the same name"
    );
    assert!(p_drop_index < p_create_index, "drop strictly before recreate, as elsewhere in this file");
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
