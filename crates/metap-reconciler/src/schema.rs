//! `PhysicalSchema` and friends — the common representation both `compile()` (from
//! `EntityDefinition`, the desired state) and `introspect()` (from `pg_catalog`, the actual
//! state) produce, so `diff()` only ever compares two `PhysicalSchema` values, never an
//! `EntityDefinition` against raw `pg_catalog` rows directly
//! (`docs/multi-tenant-platform-design.md` §5.2).

use std::collections::BTreeMap;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PhysicalSchema {
    pub table: String,
    pub columns: BTreeMap<String, ColumnSpec>,
    pub indexes: BTreeMap<String, IndexSpec>,
    pub foreign_keys: BTreeMap<String, FkSpec>,
    pub uniques: BTreeMap<String, UniqueSpec>,
}

impl PhysicalSchema {
    pub fn empty(table: impl Into<String>) -> Self {
        PhysicalSchema {
            table: table.into(),
            columns: BTreeMap::new(),
            indexes: BTreeMap::new(),
            foreign_keys: BTreeMap::new(),
            uniques: BTreeMap::new(),
        }
    }
}

/// A column's origin, not just its SQL type — `diff()` needs this to know a `Framework` column
/// (`id`/`tenant_id`/`data`/...) must never be dropped, and a `Generated` column's index lives on
/// an *expression* over `data`, not on the column itself (§5.6: the default promotion path is an
/// expression index, never `GENERATED ... STORED`, to avoid its `ACCESS EXCLUSIVE` table
/// rewrite — a physical `Generated` (trigger-synced) column only exists when a field declares
/// `storage: column`, see `compile::compile`'s doc comment).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ColumnOrigin {
    /// One of the fixed columns every per-entity table has, independent of `EntityField`s.
    Framework,
    /// A field's `data jsonb` payload — the escape hatch for anything not physically promoted.
    Data,
    /// A real physical column kept in sync with `data->>field` by a trigger (never Postgres's
    /// native `GENERATED ALWAYS AS (...) STORED`, which forces a full-table rewrite) —
    /// `field.storage == Some(FieldStorage::Column)` only. `backfilled` is how `diff()` tells
    /// "column exists and every pre-existing row has been backfilled" from "column exists but a
    /// backfill is still in progress or never started" (crash-resume, §5.7) — on the desired
    /// side (`compile()`) this is always `true` (the desired state has no notion of
    /// "in-progress"); on the actual side (`introspect()`) it reflects
    /// `reconciler_backfill_progress.completed`.
    Generated { source_field: String, backfilled: bool },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ColumnSpec {
    pub sql_type: String,
    pub nullable: bool,
    pub origin: ColumnOrigin,
}

/// Whether `pg_index.indisvalid` on the current row (introspected side) is `true` — a
/// `CREATE INDEX CONCURRENTLY` that failed or was interrupted leaves the index catalogued but
/// `invalid`; `diff()` treats that as "must be rebuilt", giving crash-resume for free (§5.4).
/// On the desired side this is always `true` (there is no such thing as a desired-but-invalid
/// index).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IndexSpec {
    /// The indexed expression/column list, already normalized (`normalize::normalize_expr`) —
    /// e.g. `(jsonb_extract_path_text(data, 'departmentid'))` or a plain column name.
    pub expression: String,
    pub unique: bool,
    /// `Some(method)` for a non-default access method (`gin`), `None` for the default btree.
    pub using: Option<String>,
    pub valid: bool,
}

/// Whether `pg_constraint.convalidated` is `true` (introspected side) — an FK added
/// `NOT VALID` and never validated (crash between the two steps) is caught by `diff()` as
/// "just run `VALIDATE CONSTRAINT`", not "add again" (§5.4).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FkSpec {
    pub column: String,
    pub ref_table: String,
    pub ref_column: String,
    pub on_delete: OnDelete,
    pub validated: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OnDelete {
    Restrict,
    Cascade,
    SetNull,
}

impl OnDelete {
    pub fn as_sql(self) -> &'static str {
        match self {
            OnDelete::Restrict => "RESTRICT",
            OnDelete::Cascade => "CASCADE",
            OnDelete::SetNull => "SET NULL",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UniqueSpec {
    pub columns: Vec<String>,
}

/// §5.5 — governs whether an op needs `Migrating` status set before running.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Cost {
    Instant,
    BackgroundFast,
    Heavy,
}

/// §5.6 — governs how the executor runs an op: inside the entity's transaction, on its own
/// connection with no transaction wrapper (required for `CONCURRENTLY`, which errors inside a
/// transaction block), or as a checkpointed batch loop (`backfill::run_heavy_backfill`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ExecutionMode {
    Transactional,
    NonTransactional,
    Batched,
}
