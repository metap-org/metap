# Hot Field Index Strategy: Metadata-Driven Expression Indexes

Date: 2026-08-01

Status: approved

Scope: first of four planned Phase 4 (Query Planner V1) sub-projects, in this
priority order:

1. **Hot field index strategy** (this spec)
2. Full-text search strategy (builds on this spec's expression-index infra)
3. Keyset pagination (independent, done after filter/sort fields have real indexes)
4. Report query boundary (separate service, least dependent on the above)

Each sub-project ships independently — spec, plan, implementation, verification
— before the next one is scoped, matching how Phase 3 was run.

## Motivation

Every business record lives in one generic `records` table (`src/infra/db/schema.ts`):
tenant/entity/status/code columns plus a `data jsonb` column for metadata-driven
fields. `QueryPlanner.planList` (`src/core/query/query-planner.ts`) resolves every
filterable/sortable field except `createdAt`/`updatedAt` via
`jsonb_extract_path_text(data, fieldName)` — there is no index backing any of
these lookups beyond the table's `(tenant_id, entity, status)` and
`(tenant_id, entity, created_at)` indexes. `EntityField` already declares
`indexed?: boolean` and `unique?: boolean` per field (`src/core/metadata/entity.ts`),
and `crm.customers`' `code`/`status` fields already set `indexed: true` — but
nothing in the codebase reads either flag. It's a documented, not-yet-built gap:
`docs/architectures/index.md`'s Data Model Strategy explicitly plans "indexed generated
columns for hot fields" as the evolution step after the current generic-JSONB
baseline.

The complication: `records` is one physical table shared by every entity, so a
naive "add a real column per indexed field" approach doesn't scale — two
unrelated entities could both declare a field named `region` with completely
different meanings, and the table would grow a column per field-name ever
declared across the whole platform. The design below indexes the JSONB path
directly, scoped per entity, instead of adding physical columns.

## Design

### Where the index specification comes from

No new metadata shape. `EntityField.indexed`/`EntityField.unique` (already
declared, currently inert) become the source of truth. A field with neither
flag set gets no index — this is opt-in, matching how `searchable`/`sortable`
already work for filter/sort allowlisting.

### Index kind: per-entity partial expression indexes, not generated columns

For a field `f` on entity `e`:

```sql
-- indexed: true
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_records_<e_sanitized>_<f>
  ON records ((jsonb_extract_path_text(data, '<f>')))
  WHERE entity = '<e>' AND deleted = false;

-- unique: true (tenant-scoped — uniqueness is never global, per
-- docs/architectures/index.md's "every business query includes tenant scope")
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uniq_records_<e_sanitized>_<f>
  ON records (tenant_id, (jsonb_extract_path_text(data, '<f>')))
  WHERE entity = '<e>' AND deleted = false;
```

The indexed expression must be `jsonb_extract_path_text(data, '<f>')`, not the
`data->>'<f>'` operator form, even though both are semantically equivalent.
Postgres only selects an expression index when the query's expression is
*syntactically* identical to the indexed one — `QueryPlanner`'s and
`condition-to-sql.ts`'s existing `fieldExpression()` helpers already build
every filter/sort condition as `jsonb_extract_path_text(data, fieldName)`, so
the index has to match that exact form or it is never chosen (confirmed via
`EXPLAIN` with `enable_seqscan` disabled during implementation — an index
built on `data->>'<f>'` was silently never used by the existing query paths).

`<e_sanitized>` replaces `.` with `_` (entity names are dot-namespaced, e.g.
`crm.customers`) to produce a valid, deterministic Postgres identifier. The
`WHERE entity = '<e>'` predicate is what keeps two different entities' indexes
on a field with the same name from colliding or interfering — each is its own
independent partial index. `AND deleted = false` matches the same soft-delete
predicate every other query already filters on, keeping the index's row set
aligned with what queries actually scan.

Postgres has no bind-parameter mechanism for identifiers (index name, the
`jsonb_extract_path_text` path key) or for the `entity =` literal used this
way — those parts of the statement are necessarily built via string
interpolation, not Drizzle's `sql` bound-parameter form. This is safe under
the same trust model `query-planner-hardening-design.md` already relies on
for field names:
`entity.name`/`field.name` only ever come from server-authored `*.entity.ts`
modules, validated by `MetadataCompiler.validate` at `MetadataRegistry.register()`
time (`src/core/metadata/metadata-compiler.ts`) — never from client/request
input. `IndexReconciler` must not be handed anything derived from a request.

Composite (multi-field) indexes are out of scope for this pass — nothing in
the codebase needs one yet, and the single-field case above is what unblocks
sub-project 2 (full-text search) and covers today's real usage (`code`,
`status`). Add composite index declarations to `EntityField`/`EntityDefinition`
later, when a concrete slow query needs one.

### Applying indexes: `IndexReconciler`, wired like `MetadataDriftService`

A new `IndexReconciler` (`src/core/metadata/index-reconciler.ts`), constructed
in `container.ts` with `db` and exposed as `container.indexReconciler` —
same shape as `MetadataDriftService`/`container.metadataDrift` from the
Phase 2 metadata compiler work:

```ts
class IndexReconciler {
  constructor(private readonly db: Database) {}

  async reconcile(
    entities: readonly EntitySummary[],
    log: { info: (obj: unknown, msg: string) => void; warn: (obj: unknown, msg: string) => void },
  ): Promise<void>
}
```

For each entity, for each field with `indexed` or `unique` set, run the
corresponding `CREATE INDEX CONCURRENTLY IF NOT EXISTS` statement. `CONCURRENTLY`
means index creation never blocks concurrent reads/writes on `records`, so this
is safe to run automatically at boot in every environment — no dev/prod split
needed. `IF NOT EXISTS` makes it idempotent: safe to run on every boot, safe if
multiple instances boot concurrently and race (Postgres itself resolves the
race; a losing racer's statement becomes a no-op).

Wired into `buildApp` (`src/server/app.ts`) right after `container.metadataDrift.check(...)`:
best-effort — wrapped in try/catch, logs and continues on any DB error, never
crashes startup. This mirrors both `MetadataDriftService`'s and `HealthService`'s
established graceful-degradation stance in this codebase.

One Postgres constraint that shapes the implementation: `CREATE INDEX
CONCURRENTLY` cannot run inside a transaction block. Each statement is
executed as its own standalone `db.client.execute(sql\`...\`)` call, not batched
inside `db.client.transaction(...)`.

### Manual mode

A thin CLI script (`scripts/reconcile-indexes.mjs`, following the existing
`scripts/seed-admin.mjs` pattern of a standalone script that builds a container
without booting the HTTP server) calls the same `IndexReconciler.reconcile`
directly. This covers running reconciliation from CI/ops tooling without
starting the API process — same code path as the automatic boot-time call, no
separate SQL-generation logic to keep in sync.

## Out of scope (deliberate, not an oversight)

- **Composite/multi-field indexes.** Single-field only for now; revisit when a
  real query needs one.
- **GIN indexes for full-text search.** Sub-project 2. This spec's expression-index
  mechanism is what sub-project 2 extends (same per-entity partial-index shape,
  different index type/expression).
- **Dropping indexes when a field's `indexed`/`unique` flag is removed from
  metadata.** `IndexReconciler` only ever adds; a field metadata deletion or
  flag removal leaves the old index in place (harmless, just unused disk space)
  until a human cleans it up. Automatic teardown risks dropping an index a
  human added directly for an unrelated reason — out of scope until that
  actually causes pain.
- **Replacing `records.code`/`records.status`'s existing physical columns**
  with this mechanism. Those two remain real top-level columns (already
  indexed via the table's existing composite indexes) — this spec is about the
  *other* metadata-driven fields inside `data` that have no index today.

## Testing (minimal — important cases only)

Following the live-DB pattern in `src/core/metadata/metadata-drift.test.ts`:

- One test: reconciling an entity with an `indexed: true` field creates the
  expected partial index (assert against `pg_indexes`).
- One test: reconciling an entity with a `unique: true` field creates a
  tenant-scoped unique partial index.
- One test: running `reconcile` twice is idempotent — second run creates
  nothing new, doesn't error.
- One test: does not throw when the database is unreachable (mirrors
  `MetadataDriftService`'s equivalent test).

No exhaustive matrix beyond that, per project convention.
