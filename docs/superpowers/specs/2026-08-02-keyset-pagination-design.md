# Keyset Pagination: Opaque Cursor Over the Resolved Sort

Date: 2026-08-02

Status: approved

Scope: third of four planned Phase 4 (Query Planner V1) sub-projects, in this
priority order:

1. Hot field index strategy (done — `docs/superpowers/specs/2026-08-01-hot-field-index-strategy-design.md`)
2. Full-text search strategy (done — `docs/superpowers/specs/2026-08-02-full-text-search-strategy-design.md`)
3. **Keyset pagination** (this spec)
4. Report query boundary (separate service, least dependent on the above)

## Motivation

`QueryPlanner.planList` (`src/core/query/query-planner.ts`) has a `limit`
(capped by `listView.maxLimit`) but no way to page past the first `limit`
rows of whatever sort order is in effect. A `cursor` field existed once and
was deliberately deleted as dead weight — `docs/superpowers/specs/2026-07-29-query-planner-hardening-design.md`:
"`ListInput.cursor` ... currently do[es] nothing ... Real keyset/cursor
pagination is a separate, later concern ... and gets reintroduced there when
it's actually built." This is that later concern. The codebase has never had
offset/page-number pagination either, so there's no competing pagination
style to reconcile — this is purely additive to `limit`.

## Design

### Cursor shape

An opaque, base64-encoded JSON object, never interpreted by the client:

```ts
type Cursor = {
  field: string;   // the resolved sort field this cursor was generated under
  value: string;    // that field's value on the last row of the previous page
  id: string;       // that row's id — tiebreaker, since sort fields aren't unique
  dir: "asc" | "desc";
};
```

Encode/decode lives in a new small file, `src/core/query/cursor.ts` —
`encodeCursor(cursor: Cursor): string` / `decodeCursor(raw: string): Cursor | undefined`
(`undefined` on any malformation: bad base64, bad JSON, missing/wrong-typed
fields, `dir` not exactly `"asc"`/`"desc"`, or `id` not matching a UUID shape
— never throws, so callers decide what "invalid" means for their context).
The UUID-shape check on `id` matters specifically: `id` is later compared
against `records.id` (a `uuid` column) as a bound parameter, and Postgres
raises a hard "invalid input syntax for type uuid" error for a non-UUID
value at query time rather than simply not matching — catching that at
decode time keeps a garbage `id` a clean `400 invalid_cursor` instead of an
unhandled 500.

### Keyset condition: matches the existing tiebreaker exactly

`QueryPlanner.planList` already always appends `asc(records.id)` as a
secondary sort key *regardless of the primary field's direction*
(`orderBy: [resolvedSort.descending ? desc(sortExpr) : asc(sortExpr), asc(records.id)]`).
A cursor's WHERE condition has to reproduce that same mixed-direction
ordering, which a single Postgres row-value comparison (`(a, b) > (x, y)`)
cannot express when `a` and `b` sort in different directions. So the
condition is built as an explicit two-clause OR instead:

```sql
-- resolved sort is descending (e.g. default -createdAt):
(sortExpr < cursor.value) OR (sortExpr = cursor.value AND id > cursor.id)

-- resolved sort is ascending:
(sortExpr > cursor.value) OR (sortExpr = cursor.value AND id > cursor.id)
```

`id` is always compared with `>` — matching the orderBy's constant `asc(records.id)`
tiebreaker no matter which way the primary field sorts.

### Cursor validity: checked against the *resolved* sort, not the raw request

`planList` already resolves the effective sort (client `sort` → entity
`defaultSort` → `"-createdAt"`) before this point in the function. A decoded
cursor's `field`/`dir` are compared against that resolved sort, not the raw
`input.sort` string — so a cursor generated under `sort=name` sent alongside
an *invalid* `sort=bogus` (which itself falls back to `-createdAt`) is
correctly judged against `-createdAt`, not `name`, and rejected. Any
mismatch — decode failure, wrong field, wrong direction — makes `planList`
throw a new `InvalidCursorError` (defined in `query-planner.ts`, alongside
the existing "Entity not found" `Error` throw already in this function).

### Executing the +1 lookahead and building `nextCursor`

`QueryPlanner.planList`'s returned `limit` is unchanged — still the
client-requested/capped page size, used for the response's `page.limit` as
today. The "fetch one extra row to know if there's a next page" trick is a
`CrudService.list`-level concern (it already owns query execution and
response shaping; `QueryPlanner` only plans SQL, never executes it — existing
boundary, unchanged):

1. `CrudService.list` queries with `.limit(plan.limit + 1)`.
2. If the result has `plan.limit + 1` rows: drop the last one, and build
   `nextCursor` from the *last row of the trimmed set* (`resolvedSort.field`,
   that row's value for it, that row's `id`, `resolvedSort.descending`).
   `QueryPlanner.planList`'s return type grows a `resolvedSort: { field: string; descending: boolean }`
   so `CrudService` doesn't have to re-derive it.
3. Otherwise: `nextCursor: null`.

`ServiceResult`'s `page` (currently `{ limit }`, loosely typed as `unknown`
in the `ServiceResult<T>` union) becomes `{ limit: number; nextCursor: string | null }`
for `CrudService.list`'s call site specifically — `ServiceResult.page`
itself stays `unknown` at the type level (unchanged, shared by every
service method), same as today.

### Wiring

- `ListInput` (`query-planner.ts`) gains `cursor?: string`.
- `ListQuerySchema`/`reservedKeys` (`src/server/routes/records.ts`) gain
  `cursor: z.string().optional()`, passed through to `ListInput.cursor` the
  same way `sort` already is.
- `CrudService.list` wraps its `queryPlanner.planList(...)` call in a
  try/catch for `InvalidCursorError` specifically, returning
  `{ ok: false, status: 400, error: "invalid_cursor", message: <decode/mismatch reason> }`
  — same shape every other `CrudService` validation failure already uses
  (`entity_not_found`, `validation_failed`, etc.), handled by the existing
  generic `sendServiceError` in `src/server/error-handler.ts` with no new
  special-casing needed there.

### Type coercion across sort field kinds

Cursor `value` is always serialized as a string (from JSON). For a
JSONB-extracted field (`jsonb_extract_path_text(...)`, always text) this
needs no coercion. For `createdAt`/`updatedAt` (real `timestamptz` columns),
the ISO-string cursor value is passed as an ordinary Drizzle bound parameter
compared against a timestamp column — Postgres infers the parameter's type
from the column it's compared to (untyped protocol-level parameters), so no
explicit cast is needed. Verified as part of Testing below, since the
default sort (`-createdAt`) is exactly this case.

## Out of scope (deliberate, not an oversight)

- Backward pagination ("previous page"). Only forward (`nextCursor`) —
  nothing in this codebase's UI or API consumers has asked for backward
  paging, and it roughly doubles the cursor/comparison logic (needs a mirrored
  `<`/`>` flip plus a "was there a previous page" lookahead in the other
  direction).
- A `hasMore`/`totalCount` field. `nextCursor: string | null` is a sufficient
  continuation signal on its own; a separate boolean would be redundant, and
  a total count requires a second query this codebase has no other reason to
  run per list request.
- Multi-column/compound sort (sorting by more than one entity field at once).
  `QueryPlanner` only supports a single sort field today; cursor pagination
  rides on top of whatever sort exists rather than expanding it.
- Cursor tampering *detection* beyond structural validation (e.g. HMAC-signing
  the cursor so a client can't hand-craft one). The cursor only ever encodes
  a sort field name, a value, an id, and a direction — all things already
  reachable more directly via ordinary `filters`/`sort` query params, so a
  forged cursor grants no capability beyond what the API already exposes.

## Testing (minimal — important cases only)

- One test: paging through more rows than fit in one `limit` — using
  `nextCursor` from page 1 to fetch page 2 returns the next distinct rows,
  with no overlap and no gaps, for the default sort (`-createdAt`), proving
  the `timestamptz` coercion case from the Design section works.
- One test: same paging behavior for a JSONB-backed sort field (e.g. `name`),
  proving the `jsonb_extract_path_text` case.
- One test: a well-formed cursor whose `field` doesn't match the resolved
  sort is rejected with `400 invalid_cursor`.
- One test: a garbage (non-base64/non-JSON) cursor string is rejected with
  `400 invalid_cursor`, not a 500.
- One test: the last page returns `nextCursor: null`.

No exhaustive matrix beyond that, per project convention.
