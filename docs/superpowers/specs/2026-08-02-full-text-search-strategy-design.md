# Full-Text Search Strategy: Opt-In `tsvector`/GIN Matching Per Field

Date: 2026-08-02

Status: approved

Scope: second of four planned Phase 4 (Query Planner V1) sub-projects, in this
priority order:

1. Hot field index strategy (done — `docs/superpowers/specs/2026-08-01-hot-field-index-strategy-design.md`)
2. **Full-text search strategy** (this spec)
3. Keyset pagination (independent, done after filter/sort fields have real indexes)
4. Report query boundary (separate service, least dependent on the above)

## Motivation

`QueryPlanner.planList` (`src/core/query/query-planner.ts`) matches every field
declared `searchable: true` with a case-insensitive substring filter
(`jsonb_extract_path_text(...) ILIKE '%value%'`). This has no real Postgres
full-text search behind it — no `tsvector`, no GIN index, no multi-word/
stemmed matching. `docs/roadmap.md` Phase 4 lists "Add full-text search
strategy" as a still-open goal.

`crm.customers` currently marks `code`, `name`, `phone`, `email` as
`searchable: true`. Only `name` is genuinely free-text (a multi-word business
name); `code`/`phone`/`email` behave like identifiers where users expect
partial/substring matching (`"E00"` finding `"E001"`) — something Postgres's
`to_tsquery` family cannot do (it matches whole lexemes, not mid-token
substrings). Converting every `searchable` field to FTS uniformly would
silently break that identifier-style matching. This spec adds FTS as an
explicit per-field opt-in, so `searchable` fields default to keeping today's
substring behavior unless a field specifically asks for FTS.

## Design

### Metadata: `EntityField.searchMode`

```ts
export type EntityField = {
  // ...existing fields
  searchMode?: "substring" | "fts"; // default: "substring"
};
```

Only meaningful when `searchable: true`; a field with `searchable: true` and
no `searchMode` (or `searchMode: "substring"`) keeps today's exact ILIKE
behavior — no change for `crm.customers`' four existing searchable fields
unless one is explicitly switched to `"fts"` later, entity-author's choice,
not part of this sub-project.

### Match semantics for `searchMode: "fts"` fields

`QueryPlanner.planList`'s per-filter branch grows a third case (alongside the
existing exact-equality and ILIKE-substring branches):

```sql
to_tsvector('simple', data->>'<field>') @@ plainto_tsquery('simple', <value>)
```

`plainto_tsquery`, not `to_tsquery` — `to_tsquery` accepts client-controlled
boolean/proximity operators (`&`, `|`, `<->`) as part of its input syntax,
which would let a filter value smuggle query operators the same way the
legacy MongoDB system this codebase explicitly avoided let object-shaped
values smuggle operators (see `docs/superpowers/specs/2026-07-29-query-planner-hardening-design.md`'s
Motivation). `plainto_tsquery` treats its entire input as plain text, ANDing
the extracted lexemes — no operator injection surface.

`'simple'` text search config: no stemming, no stopword removal. Chosen
because this platform makes no language guarantee about entity data (see
`docs/architecture.md`'s "Target Architecture: Multi-Service Evolution" —
Metap is meant to back a multi-purpose low-code platform, not an
English-only CRM demo); `'english'` stemming/stopwords would silently
misbehave on non-English content.

### Indexing: extend `IndexReconciler`, don't fork it

`IndexReconciler` (`src/core/metadata/index-reconciler.ts`, sub-project 1)
gains a third case in its per-field loop, alongside `indexed`/`unique`: a
field with `searchMode === "fts"` gets a GIN expression index, same
per-entity partial-index shape as the other two:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS gin_records_<e_sanitized>_<f>
  ON records USING GIN (to_tsvector('simple', data->>'<f>'))
  WHERE entity = '<e>' AND deleted = false;
```

Same trust model as sub-project 1: `entityName`/`fieldName` are inlined as
quoted literals (`quoteLiteral`, already implemented) because Postgres DDL
takes no bind parameters at all — safe only because they come exclusively
from server-authored, `MetadataCompiler`-validated metadata. Same
`CREATE INDEX CONCURRENTLY IF NOT EXISTS` idempotent-reconcile-at-boot
approach as the other two index kinds — no new wiring, no new container
service, `IndexReconciler.reconcile` picks this up automatically.

### What doesn't change

- No relevance ranking (`ts_rank`) and no new sort capability. `searchMode: "fts"`
  changes *matching*, not *ordering* — a matched row sorts exactly like today,
  via whatever `sort`/`defaultSort` field is already in play. Ranked/relevance
  sort is a different, larger feature (dynamic sort expressions aren't
  supported by `QueryPlanner` at all today) — out of scope here.
- No entity in this repo switches to `searchMode: "fts"` as part of this
  sub-project. This ships the mechanism; adopting it on `crm.customers` (or
  not) is a metadata-authoring decision for whoever owns that entity, same as
  how `indexed`/`unique` existed unused before sub-project 1 built the thing
  that reads them.
- `MetadataCompiler.validate` is not extended to cross-check `searchMode` against
  `searchable` (e.g. reject `searchMode: "fts"` without `searchable: true`).
  Worth adding if it turns out to be a common mistake; not needed for a
  single opt-in field flag with a safe default.

## Out of scope (deliberate, not an oversight)

- Ranking/relevance sort (`ts_rank`) — see above.
- Combined multi-field search documents (one `tsvector` covering several
  fields at once, e.g. for a single free-text `?q=` box). Each `fts` field
  gets its own independent `tsvector`/GIN pair, matching how `searchable`/
  `indexed`/`unique` are already all independent per-field flags today.
  Nothing in this codebase has asked for cross-field free-text search yet.
- Prefix/partial matching within FTS (Postgres supports `to_tsquery`'s
  `:*` prefix operator, but exposing that safely to client input is its own
  design problem, and `searchMode: "substring"` already covers the
  partial-match need for fields that want it).
- Non-`'simple'` text search configs (e.g. per-tenant/per-locale config
  selection). No current requirement; `'simple'` is the safe default until
  one exists.

## Testing (minimal — important cases only)

- One test: `QueryPlanner.planList` builds a `to_tsvector(...) @@ plainto_tsquery(...)`
  clause for a `searchMode: "fts"` field, not the ILIKE clause.
- One test: a `searchable: true` field with no `searchMode` (or `"substring"`)
  still builds the existing ILIKE clause — no regression for `crm.customers`.
- One test (in `index-reconciler.test.ts`, following the sub-project 1
  pattern): `IndexReconciler.reconcile` creates a GIN index for a
  `searchMode: "fts"` field (assert against `pg_indexes`, `indexdef` contains
  `"gin"`).
- One live-DB test exercising an actual `plainto_tsquery` match end-to-end
  (e.g. via `CrudService.list`) to prove the SQL is valid and matches
  multi-word input correctly, not just that the right SQL shape gets built.

No exhaustive matrix beyond that, per project convention.
