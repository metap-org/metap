// Package reconciler is the table-per-entity reconciler: reconcile(desired) = introspect(actual)
// -> diff -> plan -> execute for one (tenant, entity), declarative migration ops with
// preflight/quarantine, multi-tenant fan-out (orchestrator), and the one-shot generic-table-to-
// dedicated-table data move. Ported from metap's Rust metap-reconciler crate. No HTTP, no
// business-entity knowledge — a plain library, same shape as permission/cron.
package reconciler

// PhysicalSchema is the common representation both Compile (from an EntityDefinition, the
// desired state) and Introspect (from pg_catalog, the actual state) produce, so Diff only ever
// compares two PhysicalSchema values.
type PhysicalSchema struct {
	Table       string
	Columns     map[string]ColumnSpec
	Indexes     map[string]IndexSpec
	ForeignKeys map[string]FkSpec
	Uniques     map[string]UniqueSpec
}

func EmptySchema(table string) PhysicalSchema {
	return PhysicalSchema{
		Table:       table,
		Columns:     map[string]ColumnSpec{},
		Indexes:     map[string]IndexSpec{},
		ForeignKeys: map[string]FkSpec{},
		Uniques:     map[string]UniqueSpec{},
	}
}

// ColumnOriginKind is a column's origin, not just its SQL type — Diff needs this to know a
// Framework column must never be dropped, and a Generated column's index lives on an
// *expression* over data, not on the column itself.
type ColumnOriginKind int

const (
	// OriginFramework: one of the fixed columns every per-entity table has.
	OriginFramework ColumnOriginKind = iota
	// OriginData: a field's data jsonb payload — the escape hatch for anything not physically
	// promoted.
	OriginData
	// OriginGenerated: a real physical column kept in sync with data->>field by a trigger (never
	// Postgres's native GENERATED ALWAYS AS (...) STORED, which forces a full-table rewrite) —
	// field.Storage == StorageColumn only.
	OriginGenerated
)

// ColumnOrigin mirrors the Rust ColumnOrigin enum — SourceField/Backfilled are only meaningful
// when Kind == OriginGenerated.
type ColumnOrigin struct {
	Kind ColumnOriginKind
	// SourceField: the field this column is synced from (Generated only).
	SourceField string
	// Backfilled: "column exists and every pre-existing row has been backfilled" vs. "column
	// exists but a backfill is still in progress or never started" (crash-resume). On the
	// desired side (Compile) this is always true; on the actual side (Introspect) it reflects
	// reconciler_backfill_progress.completed.
	Backfilled bool
}

type ColumnSpec struct {
	SQLType  string
	Nullable bool
	Origin   ColumnOrigin
}

// IndexSpec mirrors the Rust IndexSpec. Using/WhereClause are plain strings rather than *string
// (a deliberate divergence from this codebase's usual Option<T>-as-pointer convention) — neither
// field has a real "set but empty" case worth distinguishing from "unset": an access method name
// and a WHERE predicate are never legitimately the empty string.
type IndexSpec struct {
	// Expression: the indexed expression/column list, already normalized — e.g.
	// (jsonb_extract_path_text(data, 'departmentid')) or a plain column name.
	Expression string
	Unique     bool
	// Using: non-empty for a non-default access method (gin), empty for the default btree.
	Using string
	// Valid: whether pg_index.indisvalid is true (introspected side) — a CREATE INDEX
	// CONCURRENTLY that failed or was interrupted leaves the index catalogued but invalid.
	Valid bool
	// WhereClause: a partial index's WHERE predicate (unnormalized SQL text), "" for a
	// full-table index.
	WhereClause string
}

// FkSpec mirrors the Rust FkSpec.
type FkSpec struct {
	Column    string
	RefTable  string
	RefColumn string
	OnDelete  OnDelete
	// Validated: whether pg_constraint.convalidated is true (introspected side) — an FK added
	// NOT VALID and never validated is caught by Diff as "just run VALIDATE CONSTRAINT".
	Validated bool
}

type OnDelete int

const (
	OnDeleteRestrict OnDelete = iota
	OnDeleteCascade
	OnDeleteSetNull
)

func (o OnDelete) SQL() string {
	switch o {
	case OnDeleteCascade:
		return "CASCADE"
	case OnDeleteSetNull:
		return "SET NULL"
	default:
		return "RESTRICT"
	}
}

type UniqueSpec struct {
	Columns []string
}

// Cost governs whether an op needs Migrating status set before running.
type Cost int

const (
	CostInstant Cost = iota
	CostBackgroundFast
	CostHeavy
)

// ExecutionMode governs how the executor runs an op: inside the entity's transaction, on its own
// connection with no transaction wrapper (required for CONCURRENTLY), or as a checkpointed batch
// loop.
type ExecutionMode int

const (
	ModeTransactional ExecutionMode = iota
	ModeNonTransactional
	ModeBatched
)
