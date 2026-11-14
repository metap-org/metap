// Package reconciler is the table-per-entity reconciler: reconcile(desired) = introspect(actual) ->
// diff -> plan -> execute for one (tenant, entity), declarative migration ops with
// preflight/quarantine, multi-tenant fan-out (orchestrator), and the one-shot generic-table-to-
// dedicated-table data move. No HTTP, no business-entity knowledge — a plain library, same shape as
// permission/cron.
package reconciler

// PhysicalSchema is the common representation both Compile (from an EntityDefinition, the
// desired state) and Introspect (from pg_catalog, the actual state) produce, so Diff only ever
// compares two PhysicalSchema values.
type PhysicalSchema struct {
	Table string
	// HasLegacyData: the introspected table still carries the pre-Phase-106 `data jsonb` column,
	// i.e. its rows have not been moved to native columns yet. Never set on a desired schema.
	HasLegacyData bool
	// LegacySyncTriggers: field names that still have a pre-Phase-106 trigger keeping a real
	// column in sync with data ->> field. Only meaningful alongside HasLegacyData.
	LegacySyncTriggers []string
	Columns            map[string]ColumnSpec
	Indexes            map[string]IndexSpec
	ForeignKeys        map[string]FkSpec
	Uniques            map[string]UniqueSpec
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
// Framework column must never be dropped.
type ColumnOriginKind int

const (
	// OriginFramework: one of the fixed columns every per-entity table has.
	OriginFramework ColumnOriginKind = iota
	// OriginNative: the column that holds one EntityDefinition field's value (Phase 106 — every
	// field has its own typed column; there is no data jsonb payload any more).
	OriginNative
)

// ColumnOrigin: Backfilled is only meaningful for an OriginNative column on a table that still
// has the legacy data column — "this column has been filled from data for every pre-existing
// row" (reconciler_backfill_progress.completed) vs. "a backfill is still in progress or never
// started" (crash-resume). On the desired side (Compile) it is always true.
type ColumnOrigin struct {
	Kind       ColumnOriginKind
	Backfilled bool
}

type ColumnSpec struct {
	SQLType  string
	Nullable bool
	Origin   ColumnOrigin
}

// Using/WhereClause are plain strings rather than *string (a deliberate divergence from this
// codebase's usual Option<T>-as-pointer convention) — neither field has a real "set but empty" case
// worth distinguishing from "unset": an access method name and a WHERE predicate are never
// legitimately the empty string.
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
