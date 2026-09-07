//! `compile(desired) -> PhysicalSchema` — the desired-state half of the reconcile pipeline
//! (`docs/multi-tenant-platform-design.md` §5.2). A pure function: same `EntityDefinition`
//! always compiles to the same `PhysicalSchema`, no I/O.

use anyhow::bail;
use metap_metadata::{
    field_has_real_column, field_kind_sql_type, resolve_field_storage_tier, EntityDefinition, FieldKind,
    FieldStorageTier,
};

use crate::schema::{ColumnOrigin, ColumnSpec, FkSpec, IndexSpec, OnDelete, PhysicalSchema};

/// The fixed columns every per-entity table has, independent of `EntityField`s — same shape as
/// the shared `records` table (`crates/migrations/0000_green_jean_grey.sql`) minus `entity`
/// (one table = one entity now, no discriminator needed).
pub const FRAMEWORK_COLUMNS: &[(&str, &str, bool)] = &[
    ("id", "uuid", false),
    ("tenant_id", "uuid", false),
    ("code", "varchar(120)", true),
    ("status", "varchar(80)", true),
    ("data", "jsonb", false),
    ("version", "integer", false),
    ("deleted", "boolean", false),
    ("created_at", "timestamptz", false),
    ("updated_at", "timestamptz", false),
    ("created_by", "uuid", true),
    ("updated_by", "uuid", true),
];

/// Every table-per-entity table lives in this schema, never `public` — `public` is where the
/// shared `records`/`users`/`policies`/... framework tables that any tenant DB gets from
/// `crates/migrations/*.sql` live (real feedback: mixing a tenant's own business tables into
/// the same schema as the platform's operational tables made the boundary between "framework"
/// and "this tenant's actual data" invisible just from `\dt`).
pub const ENTITY_SCHEMA: &str = "entities";

/// `crates/metap-metadata/src/entity.rs`'s `EntityDefinition.name` is a dotted namespace
/// (`"hr.employees"`) — not a valid unquoted SQL identifier. One per-entity table per entity,
/// name-mangled the same way `metap-peripherals`'s index names already do
/// (`crates/metap-peripherals/src/index_reconciler.rs`'s `build_index_name`). Bare (unqualified)
/// — used for index/trigger/function naming, where a schema prefix would just be noise; see
/// `qualified_table_name_for` for the actual physical table identifier.
pub fn table_name_for(entity_name: &str) -> String {
    entity_name.replace('.', "_")
}

/// The actual physical table identifier `PhysicalSchema.table`/`EntityDefinition.table_name`
/// use — `ENTITY_SCHEMA` + `table_name_for`'s mangled name, e.g. `"entities.jira_issues"`.
pub fn qualified_table_name_for(entity_name: &str) -> String {
    format!("{ENTITY_SCHEMA}.{}", table_name_for(entity_name))
}

/// Postgres silently truncates an identifier over 63 bytes rather than erroring — two entity
/// names differing only after byte 63 of their mangled form (`table_name_for`) would then
/// collide on one physical table with no warning. Only mattered in theory while every
/// `table_name_for` input was a short, developer-chosen Rust literal; now that a low-code
/// entity's operator-supplied name reaches this same path (`metap-lowcode`'s
/// `LowCodeEntityDefinition`), it's worth a real check rather than relying on Postgres's silent
/// truncation to never collide by luck.
pub fn check_table_name_length(entity_name: &str) -> anyhow::Result<()> {
    let mangled = table_name_for(entity_name);
    if mangled.len() > 63 {
        bail!(
            "entity name '{entity_name}' mangles to a {}-byte table name ('{mangled}'), over Postgres's 63-byte identifier limit — choose a shorter entity name",
            mangled.len()
        );
    }
    Ok(())
}

fn index_name(entity_name: &str, field_name: &str, kind: &str) -> String {
    format!("{kind}_{}_{field_name}", table_name_for(entity_name))
}

/// FNV-1a, not `std::hash::DefaultHasher` — the latter's algorithm isn't guaranteed stable
/// across Rust/std versions (documented explicitly), which would silently break reconcile
/// convergence for any entity whose composite index name needs the hash fallback below (a
/// different hash after a toolchain upgrade = a "new" desired name = the old index orphaned,
/// never cleaned up automatically the way a same-named rebuild is).
fn fnv1a(bytes: &[u8]) -> u32 {
    let mut hash: u32 = 0x811c9dc5;
    for &b in bytes {
        hash ^= b as u32;
        hash = hash.wrapping_mul(0x01000193);
    }
    hash
}

/// Deterministic name for a composite `unique_constraints` entry — the same `uniq_<table>_<field>`
/// shape a single `unique: true` field gets (`index_name`), extended to join every field in the
/// group with `_`. Postgres silently truncates an identifier over 63 bytes (same class of risk
/// `check_table_name_length` guards against for table names) — unlike a table name, an over-long
/// composite index name is a real possibility even with short table/field names once 3-4 fields
/// are involved, so this truncates deterministically with a short hash suffix rather than
/// rejecting the entity outright (a name collision is the actual failure mode to prevent, not
/// length by itself).
fn composite_unique_index_name(entity_name: &str, fields: &[String]) -> String {
    let full = format!("uniq_{}_{}", table_name_for(entity_name), fields.join("_"));
    if full.len() <= 63 {
        return full;
    }
    let suffix = format!("_{:08x}", fnv1a(full.as_bytes()));
    let keep = 63 - suffix.len();
    let mut end = keep.min(full.len());
    while end > 0 && !full.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}{}", &full[..end], suffix)
}

/// The expression a field contributes to an index — same shape whether it's this field's own
/// promoted single-field index or one of several fields inside a composite `unique_constraints`
/// entry. A quoted column reference for a real (FK/`storage: column`) column, otherwise the same
/// JSONB expression form the per-field loop below builds inline (kept identical, including the
/// `Date`/`Datetime` uncast special case — see that loop's own comment for why).
fn field_index_expression(field: &metap_metadata::EntityField) -> String {
    if field_has_real_column(field) {
        format!("\"{}\"", field.name)
    } else {
        match field.kind {
            FieldKind::Date | FieldKind::Datetime => format!("(data ->> '{}')", field.name),
            _ => format!("((data ->> '{}')::{})", field.name, field_kind_sql_type(field.kind)),
        }
    }
}

/// Compiles an `EntityDefinition` into its target `PhysicalSchema` under table-per-entity.
///
/// - Every `Reference` field with a `ref_entity` gets a real FK (`ColumnOrigin`/tier-independent
///   — referential integrity, not a query-optimization decision; §3.3).
/// - `field.storage == Some(FieldStorage::Column)` is the only thing that produces a real,
///   trigger-synced physical column (`ColumnOrigin::Generated`) — every other promoted field
///   (`resolve_field_storage_tier` returning `GeneratedColumn`) gets an **expression index**
///   straight over `data`, never a new column, per §5.6's "avoid `GENERATED ... STORED`'s
///   `ACCESS EXCLUSIVE` table rewrite by default" rule.
/// - `searchable` fields always use an expression-based GIN index (tsvector for
///   `search_mode: fts`, trigram otherwise) regardless of `storage` — a real column doesn't
///   remove the need for the derived search expression.
pub fn compile(entity: &EntityDefinition) -> anyhow::Result<PhysicalSchema> {
    check_table_name_length(&entity.name)?;
    let mut schema = PhysicalSchema::empty(qualified_table_name_for(&entity.name));

    let framework_names: std::collections::HashSet<&str> = FRAMEWORK_COLUMNS.iter().map(|(name, ..)| *name).collect();
    for (name, sql_type, nullable) in FRAMEWORK_COLUMNS {
        schema.columns.insert(
            name.to_string(),
            ColumnSpec {
                sql_type: sql_type.to_string(),
                nullable: *nullable,
                origin: ColumnOrigin::Framework,
            },
        );
    }

    for field in &entity.fields {
        // `code`/`status` are the two framework columns an `EntityField` is *expected* to name-
        // collide with — `metap-crud`'s `CrudService` already mirrors a `code`-holding or
        // workflow-state field's value into these directly (`data.get("code")`,
        // `get_initial_status`), the same way it does for the shared `records` table, completely
        // independent of this compiler's per-field column-promotion/trigger machinery. Every
        // other framework name has no such mirror and stays a hard collision.
        if field.name != "code" && field.name != "status" && framework_names.contains(field.name.as_str()) {
            bail!(
                "entity '{}' field '{}' collides with a framework column name",
                entity.name,
                field.name
            );
        }
        if field.name == "code" || field.name == "status" {
            continue;
        }

        let sql_type = field_kind_sql_type(field.kind);

        // `searchable` always uses an expression-based GIN index straight over `data` —
        // independent of every other flag. Does *not* unconditionally `continue` past the rest
        // of this loop body anymore: a field can be both `searchable` and `unique` (`waf.zones`'s
        // `hostname`, real-world case) — the two need genuinely separate index objects (a GIN
        // trigram/tsvector index can't enforce uniqueness), so a `unique: true` field still needs
        // to fall through into the unique-handling logic below. Found live, 2026-09-07: the old
        // unconditional `continue` here silently dropped `hostname`'s uniqueness entirely once
        // `waf.zones` moved to table-per-entity — it was enforced on the old shared `records`
        // table (built by the unrelated `metap-peripherals::index_reconciler` path, which has no
        // such conflict), so this was a real regression this migration introduced, not a
        // pre-existing gap.
        if field.searchable == Some(true) {
            let fts = field.search_mode.as_deref() == Some("fts");
            let (kind, expr) = if fts {
                ("gin", format!("to_tsvector('simple', (data ->> '{}'))", field.name))
            } else {
                ("trgm", format!("(data ->> '{}') gin_trgm_ops", field.name))
            };
            schema.indexes.insert(
                index_name(&entity.name, &field.name, kind),
                IndexSpec {
                    expression: expr,
                    unique: false,
                    using: Some("gin".to_string()),
                    valid: true,
                    where_clause: None,
                },
            );
            if field.unique != Some(true) {
                continue;
            }
        }

        // A real SQL `FOREIGN KEY` needs a real physical column to attach to — a `Reference`
        // field can never stay JSONB-only once its `ref_entity` is set, regardless of
        // `indexed`/`storage` (§3.3: "tách bảng → FK thật cấp DB"). Every other promoted field
        // stays index-only by default (§5.6) — this is the one exception, forced by what a FK
        // constraint physically requires, not a query-optimization choice.
        let is_fk_reference = field.kind == FieldKind::Reference && field.ref_entity.is_some();
        // Single source of truth shared with `metap-query` (`crates/metap-metadata/src/entity.rs`'s
        // `field_has_real_column`) so DDL and SQL generation can never disagree about which
        // fields have a real column.
        let wants_real_column = field_has_real_column(field);

        if !wants_real_column && resolve_field_storage_tier(field) == FieldStorageTier::Jsonb {
            continue;
        }

        let unique = field.unique == Some(true);
        let indexed = unique || field.indexed == Some(true) || field.sortable == Some(true) || is_fk_reference;

        if wants_real_column {
            schema.columns.insert(
                field.name.clone(),
                ColumnSpec {
                    sql_type: sql_type.to_string(),
                    nullable: true,
                    origin: ColumnOrigin::Generated {
                        source_field: field.name.clone(),
                        backfilled: true,
                    },
                },
            );
            // A `unique: true` field gets a *partial* unique index (`WHERE deleted = false`),
            // never a blanket table `UNIQUE` constraint — a plain constraint can't express "not
            // among soft-deleted rows", so a deleted record would permanently occupy its unique
            // value, blocking a legitimate new row from ever reusing it. Found live (2026-09-07):
            // `metap-demo-waf`'s `waf.ddos_policies.zoneId` — a real deleted-then-recreated
            // DDoS policy hit exactly this, rejected with `unique_violation` against a row the
            // portal itself had already soft-deleted. (This single `IndexSpec` also replaces
            // what used to be two separate, redundant unique constructs for the same field —
            // see this crate's `schema.uniques`/`UniqueSpec`, now unused by `compile()`.)
            if indexed {
                schema.indexes.insert(
                    index_name(&entity.name, &field.name, if unique { "uniq" } else { "idx" }),
                    IndexSpec {
                        expression: format!("\"{}\"", field.name),
                        unique,
                        using: None,
                        valid: true,
                        where_clause: unique.then(|| "deleted = false".to_string()),
                    },
                );
            }
            if is_fk_reference {
                schema.foreign_keys.insert(
                    format!("fk_{}_{}", table_name_for(&entity.name), field.name),
                    FkSpec {
                        column: field.name.clone(),
                        ref_table: qualified_table_name_for(
                            field.ref_entity.as_deref().expect("checked by is_fk_reference"),
                        ),
                        ref_column: "id".to_string(),
                        on_delete: OnDelete::Restrict,
                        validated: true,
                    },
                );
            }
        } else {
            // `Date`/`Datetime` deliberately stay an uncast text expression here, not
            // `((data ->> 'field')::date)` like every other kind gets — found live
            // (`../metap-demo-jira`'s `dueDate` field, first `indexed`/`sortable` Date-kind field
            // anywhere in this codebase): Postgres's implicit `text -> date`/`text ->
            // timestamptz` cast (`date_in`/`timestamptz_in`) is `STABLE`, not `IMMUTABLE` — it
            // depends on the session's `DateStyle`/`TimeZone` GUCs — and `CREATE INDEX` on an
            // expression requires every function/cast in it to be `IMMUTABLE`, so the cast form
            // fails outright with "functions in index expression must be marked IMMUTABLE".
            // Dropping the cast isn't a functionality loss: `metap-query`'s
            // `condition_to_sql`/`sort_field_expression` never emit a typed date comparison for
            // a non-promoted field either (`jsonb_extract_path_text(data, ...)`, always
            // text-compared) — an uncast index actually matches what queries emit, unlike the
            // cast form ever would have, and ISO-8601 date/timestamp strings (what this
            // metadata-driven wire format always stores) sort correctly as plain text anyway.
            let expr = match field.kind {
                FieldKind::Date | FieldKind::Datetime => format!("(data ->> '{}')", field.name),
                // The extra outer paren is required, not cosmetic — probed live: Postgres's
                // `CREATE INDEX ... (expr)` grammar rejects a bare `(a ->> 'b')::type` as an
                // index key ("syntax error at or near ::"), but accepts
                // `((a ->> 'b')::type)`. A plain (uncast) expression or one that already has
                // its own enclosing structure (the trigram/tsvector cases below) doesn't hit
                // this — only a cast sitting directly at the top level does.
                _ => format!("((data ->> '{}')::{})", field.name, sql_type),
            };
            schema.indexes.insert(
                index_name(&entity.name, &field.name, if unique { "uniq" } else { "idx" }),
                IndexSpec {
                    expression: expr,
                    unique,
                    using: None,
                    valid: true,
                    // Same soft-delete-aware partial index as the real-column case above — a
                    // JSONB-only `unique: true` field has the identical "soft-deleted row
                    // permanently occupies its value" gap otherwise.
                    where_clause: unique.then(|| "deleted = false".to_string()),
                },
            );
        }
    }

    // Composite `unique_constraints` (`compiler::validate` already rejected an unknown field
    // name, a <2-field entry, or a duplicate field-set before this ever runs) — same partial
    // (`WHERE deleted = false`) shape a single-field `unique: true` gets, over all of the
    // constraint's fields' expressions joined. Deliberately a second pass over
    // `entity.unique_constraints` rather than folded into the per-field loop above: a composite
    // constraint's fields don't need to individually be `unique`/`indexed`/`sortable` to
    // participate (the blacklist/whitelist motivating case — `type`/`kind` are plain fields,
    // only their *combination* is constrained), so there's no single field iteration this could
    // hang off of.
    if !entity.unique_constraints.is_empty() {
        let field_by_name: std::collections::HashMap<&str, &metap_metadata::EntityField> =
            entity.fields.iter().map(|f| (f.name.as_str(), f)).collect();
        for constraint in &entity.unique_constraints {
            let exprs: Vec<String> = constraint
                .fields
                .iter()
                .map(|name| {
                    field_index_expression(
                        field_by_name
                            .get(name.as_str())
                            .expect("compiler::validate already rejected an unknown field name"),
                    )
                })
                .collect();
            schema.indexes.insert(
                composite_unique_index_name(&entity.name, &constraint.fields),
                IndexSpec {
                    expression: exprs.join(", "),
                    unique: true,
                    using: None,
                    valid: true,
                    where_clause: Some("deleted = false".to_string()),
                },
            );
        }
    }

    Ok(schema)
}

#[cfg(test)]
mod tests {
    use metap_metadata::{EntityField, EntityListView, FieldStorage};

    use super::*;

    fn plain_field(name: &str, kind: FieldKind) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
            computed: None,
        }
    }

    fn entity(name: &str, fields: Vec<EntityField>) -> EntityDefinition {
        EntityDefinition {
            name: name.to_string(),
            label: name.to_string(),
            table_name: "records".to_string(),
            fields,
            list_views: vec![EntityListView {
                name: "default".to_string(),
                label: "Default".to_string(),
                fields: vec![],
                filters: vec![],
                required_fields: vec![],
                default_sort: None,
                max_limit: 50,
            }],
            workflow: None,
            unique_constraints: vec![],
        }
    }

    #[test]
    fn table_name_mangles_dots() {
        assert_eq!(table_name_for("hr.employees"), "hr_employees");
    }

    #[test]
    fn framework_columns_always_present() {
        let schema = compile(&entity("hr.departments", vec![])).unwrap();
        assert_eq!(schema.table, "entities.hr_departments");
        for (name, ..) in FRAMEWORK_COLUMNS {
            assert!(schema.columns.contains_key(*name), "missing framework column {name}");
        }
    }

    #[test]
    fn unpromoted_field_gets_no_column_and_no_index() {
        let schema = compile(&entity("t.e", vec![plain_field("notes", FieldKind::String)])).unwrap();
        assert_eq!(schema.columns.len(), FRAMEWORK_COLUMNS.len());
        assert!(schema.indexes.is_empty());
    }

    #[test]
    fn indexed_field_gets_expression_index_not_a_column() {
        let mut f = plain_field("departmentId", FieldKind::String);
        f.indexed = Some(true);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        assert_eq!(schema.columns.len(), FRAMEWORK_COLUMNS.len(), "no new column expected");
        assert_eq!(schema.indexes.len(), 1);
        let idx = schema.indexes.values().next().unwrap();
        assert_eq!(idx.expression, "((data ->> 'departmentId')::text)");
        assert!(!idx.unique);
    }

    /// A cast `text -> date`/`text -> timestamptz` is `STABLE` in Postgres (depends on
    /// `DateStyle`/`TimeZone`), not `IMMUTABLE` — `CREATE INDEX` on an expression requires
    /// `IMMUTABLE`, so the generic cast-expression form every other kind gets would fail at
    /// reconcile time for `Date`/`Datetime`. Found live via `../metap-demo-jira`'s `dueDate` field.
    #[test]
    fn indexed_date_field_gets_an_uncast_text_expression_index_not_a_stable_cast() {
        let mut f = plain_field("dueDate", FieldKind::Date);
        f.indexed = Some(true);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        assert_eq!(schema.indexes.len(), 1);
        let idx = schema.indexes.values().next().unwrap();
        assert_eq!(idx.expression, "(data ->> 'dueDate')");
    }

    #[test]
    fn storage_column_override_promotes_to_a_real_column() {
        let mut f = plain_field("amount", FieldKind::Money);
        f.indexed = Some(true);
        f.storage = Some(FieldStorage::Column);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        let col = schema.columns.get("amount").expect("real column expected");
        assert_eq!(col.sql_type, "numeric(18,4)");
        assert!(matches!(col.origin, ColumnOrigin::Generated { .. }));
        assert_eq!(schema.indexes.len(), 1);
    }

    #[test]
    fn unique_storage_column_gets_a_single_partial_unique_index_not_a_blanket_constraint() {
        let mut f = plain_field("sku", FieldKind::String);
        f.unique = Some(true);
        f.storage = Some(FieldStorage::Column);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        // `schema.uniques`/`UniqueSpec` (a blanket table `UNIQUE` constraint) is no longer
        // populated by `compile()` at all — see this fix's call site comment for why a blanket
        // constraint is wrong (can't express "unique among non-deleted rows").
        assert!(schema.uniques.is_empty());
        assert_eq!(schema.indexes.len(), 1);
        let idx = schema.indexes.values().next().unwrap();
        assert!(idx.unique);
        assert_eq!(idx.where_clause.as_deref(), Some("deleted = false"));
    }

    #[test]
    fn reference_field_gets_fk_regardless_of_indexed_flag() {
        let mut f = plain_field("departmentId", FieldKind::Reference);
        f.ref_entity = Some("hr.departments".to_string());
        let schema = compile(&entity("hr.employees", vec![f])).unwrap();
        assert_eq!(schema.foreign_keys.len(), 1);
        let fk = schema.foreign_keys.values().next().unwrap();
        assert_eq!(fk.ref_table, "entities.hr_departments");
        assert_eq!(fk.ref_column, "id");
        // Not indexed/sortable/unique/searchable, but an FK column always gets a plain btree
        // index too (best practice, avoids full-table scans on parent-side operations) — it
        // must be a real column index, not an expression, since the field is now a real column.
        assert_eq!(schema.indexes.len(), 1);
        let idx = schema.indexes.values().next().unwrap();
        assert_eq!(idx.expression, "\"departmentId\"");
    }

    #[test]
    fn searchable_fts_field_gets_gin_tsvector_expression_index() {
        let mut f = plain_field("description", FieldKind::String);
        f.searchable = Some(true);
        f.search_mode = Some("fts".to_string());
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        let idx = schema.indexes.values().next().unwrap();
        assert!(idx.expression.contains("to_tsvector"));
        assert_eq!(idx.using.as_deref(), Some("gin"));
    }

    #[test]
    fn searchable_substring_field_gets_gin_trgm_expression_index() {
        let mut f = plain_field("title", FieldKind::String);
        f.searchable = Some(true);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        let idx = schema.indexes.values().next().unwrap();
        assert!(idx.expression.contains("gin_trgm_ops"));
    }

    #[test]
    fn searchable_and_unique_field_gets_both_a_gin_index_and_a_partial_unique_index() {
        // `waf.zones.hostname`'s real shape — searchable (trigram) AND unique. Used to lose
        // uniqueness entirely: the searchable branch's unconditional `continue` skipped the
        // unique-handling logic below it for any field that was also searchable.
        let mut f = plain_field("hostname", FieldKind::String);
        f.searchable = Some(true);
        f.unique = Some(true);
        let schema = compile(&entity("t.e", vec![f])).unwrap();
        assert_eq!(schema.indexes.len(), 2, "one GIN trgm index + one partial unique index");
        let gin = schema
            .indexes
            .values()
            .find(|i| i.expression.contains("gin_trgm_ops"))
            .expect("trgm index must still exist");
        assert!(!gin.unique);
        let uniq = schema
            .indexes
            .values()
            .find(|i| i.unique)
            .expect("unique index must exist despite the field also being searchable");
        assert_eq!(uniq.where_clause.as_deref(), Some("deleted = false"));
    }

    #[test]
    fn composite_unique_constraint_builds_one_partial_unique_index_over_both_fields() {
        // The blacklist/whitelist motivating case: neither `type` nor `value` alone is unique,
        // only the pair — plain JSONB fields, not individually `unique`/`indexed`.
        let mut e = entity(
            "t.e",
            vec![plain_field("type", FieldKind::String), plain_field("value", FieldKind::String)],
        );
        e.unique_constraints = vec![metap_metadata::EntityUniqueConstraint {
            fields: vec!["type".to_string(), "value".to_string()],
        }];
        let schema = compile(&e).unwrap();
        let uniq = schema
            .indexes
            .values()
            .find(|i| i.unique)
            .expect("composite unique index must exist");
        assert_eq!(uniq.where_clause.as_deref(), Some("deleted = false"));
        assert!(uniq.expression.contains("'type'") && uniq.expression.contains("'value'"));
        // Neither field gets its own single-field index — only the composite one exists.
        assert_eq!(schema.indexes.len(), 1);
    }

    #[test]
    fn composite_unique_index_name_is_deterministically_truncated_when_too_long() {
        let fields = vec!["a".repeat(30), "b".repeat(30), "c".repeat(30)];
        let name = composite_unique_index_name("t.e", &fields);
        assert!(name.len() <= 63);
        // Stable across calls — same inputs must always mangle to the same name, or reconcile
        // would never converge (a "new" name every pass looks identical to a rename to `diff()`).
        assert_eq!(name, composite_unique_index_name("t.e", &fields));
    }

    #[test]
    fn field_name_colliding_with_a_framework_column_is_rejected() {
        let err = compile(&entity("t.e", vec![plain_field("tenant_id", FieldKind::String)])).unwrap_err();
        assert!(err.to_string().contains("tenant_id"));
    }

    #[test]
    fn short_entity_name_passes_the_table_name_length_guard() {
        check_table_name_length("hr.employees").unwrap();
    }

    #[test]
    fn entity_name_mangling_to_over_63_bytes_is_rejected_not_silently_truncated() {
        let long_name = format!("test.{}", "a".repeat(70));
        let err = check_table_name_length(&long_name).unwrap_err();
        assert!(err.to_string().contains("63-byte"));
    }

    #[test]
    fn compile_rejects_an_entity_whose_table_name_would_be_too_long() {
        let long_name = format!("test.{}", "a".repeat(70));
        let err = compile(&entity(&long_name, vec![])).unwrap_err();
        assert!(err.to_string().contains("63-byte"));
    }
}
