# Field-Level + Record-Level Enforcement — Design

Date: 2026-08-01
Status: Approved, pending implementation plan

## Context

This is sub-project 3 of the 4-part "dynamic permission engine" initiative:

1. Dynamic role assignment (shipped).
2. Policy storage + RBAC/ABAC evaluator (shipped — `policies` table, `PermissionService.checkAction`, `evaluateCondition`).
3. **Field-level + record-level enforcement** (this spec).
4. `PolicyExplainer` + `PermissionSnapshotCache` + tests (next spec, depends on this one).

Sub-project 2 built entity-level RBAC/ABAC (`can{Read,Create,Update}Entity`) and a
generic `evaluateCondition(condition, subject, context)` evaluator, deliberately
designed so `subject` could later be record data instead of `context` without
changing the evaluator itself. This sub-project is where that pays off: it wires
actual field masking and record-scoped conditions into `CrudService` and
`QueryPlanner`.

## Goals

- A policy can target a specific field (`field` set) instead of the whole
  entity, with `access: "read" | "write"`.
- A policy's condition can be evaluated against the **record's data**
  (`subject: "record"`) instead of just `context` — enabling ownership-style
  rules (`record.createdBy == context.userId`) at both record scope and field
  scope.
- Read: fields the caller can't read are stripped from every record the API
  returns (list, create response, update response, transition response) —
  not just list.
- Write: if a `create`/`update` payload includes a field the caller can't
  write, the request is rejected with 403 (never silently dropped — same
  explicit-over-silent principle as Workflow Engine V1's `guard_failed`).
- Record-level conditions apply to `list()` (translated to SQL, so pagination
  stays correct — per sub-project 2's spec) and to `update()`/`transition()`
  (evaluated in memory against the already-fetched record).
- Field/record policies are fetched **once per `CrudService` call**, not once
  per row/field — this is the mechanism sub-project 4 formalizes as
  `PermissionSnapshotCache`.

## Non-goals

- A separate `GET /api/:entity/:id` single-record endpoint. One doesn't exist
  today; record-level *read* enforcement therefore only has to be wired
  through `list()`. If a single-record GET is added later, it reuses the
  same building blocks.
- Cross-request caching (TTL-based). Explicitly decided against — per-request
  fetch is enough given policies are small per-tenant tables and this avoids
  invalidation entirely.
- `PolicyExplainer` and the `/admin/policies/explain` debug endpoint —
  sub-project 4.
- Changing `checkAction`'s entity-level semantics (sub-project 2) — this
  sub-project only adds new code paths alongside it.

## Design

### 1. Schema: extend `policies`

Two new nullable columns on the existing `policies` table
(`src/infra/db/schema.ts`):

- `field` (varchar(120), nullable) — a field name from the entity's
  `fields[]`. `null` means the policy is entity-level (sub-project 2,
  unchanged). Non-null means field-level.
- `subject` (varchar(20), default `"context"`) — `"context"` or `"record"`.
  Determines what `evaluateCondition` receives as its `subject` argument.
  Existing sub-project 2 rows are unaffected (default `"context"` matches
  their actual behavior).

When `field` is set, `action` must be `"read"` or `"write"` (not
`"create"`/`"update"` — a field write policy applies uniformly to both,
there's no meaningful difference between "creating a record with this field
set" and "updating this field later"). When `field` is null, `action` keeps
its sub-project 2 meaning (`"read"|"create"|"update"`).

### 2. `QueryPlanner`: `conditionToSql`

New function (`src/core/query/condition-to-sql.ts`) that mirrors
`evaluateCondition`'s recursion structure but emits Drizzle `SQL` instead of
a boolean:

```ts
function conditionToSql(condition: PolicyCondition, context: RequestContext): SQL
```

- `{ all: [...] }` → `and(...)`
- `{ any: [...] }` → `or(...)`
- `{ attribute, op, value }` → resolves `attribute` the same way
  `fieldExpression()` already does in `query-planner.ts` (top-level
  `records` columns for known names like `createdBy`, else
  `jsonb_extract_path_text(records.data, attribute)`), resolves `value` (a
  literal, or `context[fromContext]` substituted as a bound parameter — not
  a further column reference, since context is known at request time), and
  emits `eq`/`ne`/`inArray`/`notInArray`.

`QueryPlanner.planList` gains an optional parameter for the caller's
record-level read policies (already fetched by `CrudService`); if present,
their `conditionToSql` translations are OR'd together and AND'd into the
existing `WHERE` alongside tenant/entity/deleted scoping.

### 3. `PermissionService`: field/record methods

New methods alongside `checkAction`:

- `getFieldPolicies(tenantId, entity): Promise<PolicyRow[]>` — all
  `field IS NOT NULL` rows for the entity. Fetched once per `CrudService`
  call, reused across every field/row check in that call — this is the
  "snapshot" sub-project 4 names.
- `getRecordPolicies(tenantId, entity, action): Promise<PolicyRow[]>` — all
  `field IS NULL AND subject = 'record'` rows for `(entity, action)`.
- `filterReadableFields(context, record, fieldPolicies): Record<string, unknown>`
  — for each key in `record`, if a matching `access: "read"` field policy
  exists and none of them pass (role gate + condition, condition subject =
  `record` or `context` per the policy's `subject`), the key is deleted from
  a shallow copy before returning. No matching policy for a field → field
  stays (matches the default-open behavior everywhere else in this system).
- `assertWritableFields(context, payloadFields, existingRecord, fieldPolicies): PermissionDecision`
  — for each key in `payloadFields`, if a matching `access: "write"` field
  policy exists and none pass, returns `{ allowed: false, reason:
  "forbidden" }` immediately (first failing field wins — doesn't need to
  enumerate all of them). `existingRecord` is `undefined` for `create`
  (no record yet — a `subject: "record"` write policy targeting create
  degenerates to evaluating against `{}`, which will simply fail any
  condition referencing real record attributes; documented, not a bug).
- `canUpdateRecordCondition(context, record, recordPolicies): PermissionDecision`
  — OR across `recordPolicies` exactly like `checkAction`'s existing
  per-policy loop, but with `subject = record` instead of `context`.

### 4. `CrudService` wiring

- **`list()`**: fetch record-level read policies for the entity via
  `getRecordPolicies(tenantId, entity, "read")`; pass to `QueryPlanner`
  for SQL pushdown. After the query returns rows, fetch field-level read
  policies once via `getFieldPolicies`, then `filterReadableFields` each
  row before returning.
- **`create()`**: after Zod validation, fetch field-level write policies,
  call `assertWritableFields(context, Object.keys(parsedData), undefined,
  fieldPolicies)` — 403 before the DB write if it fails. After insert,
  `filterReadableFields` the response.
- **`update()`**: after fetching the existing record (already does this for
  optimistic locking), fetch field-level write policies and record-level
  update policies. `assertWritableFields(context, Object.keys(rawData),
  existingData, fieldPolicies)` and `canUpdateRecordCondition(context,
  existingData, recordPolicies)` — both must pass, 403 if either fails.
  After update, `filterReadableFields` the response.
- **`transition()`**: fetch record-level update policies and
  `canUpdateRecordCondition` against the existing record (transitions are a
  restricted form of update — same gate). Field-level checks don't apply
  here (a transition only ever changes the state field, which isn't
  user-suppliable payload). After transition, `filterReadableFields` the
  response.

### 5. Admin API

`POST /admin/policies`'s body gains two optional fields: `field?: string`
and `subject?: "context" | "record"` (default `"context"` if omitted,
matching the schema default). No new routes — the existing
list/create/delete routes already work generically once the schema/service
support the new columns.

## Consequences for existing code

- `QueryPlanner.planList`'s signature changes (new optional parameter) —
  its one caller, `CrudService.list`, needs updating.
- `CrudService`'s four methods each gain 1-3 new `await`ed calls before
  their existing DB operations (or before their response is returned, for
  masking). None of this changes behavior for `crm.customers`, which has no
  field/record policies — every new code path's "no policies found" branch
  degenerates to today's behavior, so existing tests
  (`crud-service.test.ts`, `app.test.ts`) are expected to keep passing
  unmodified, same pattern as sub-project 2.
- `permission-service.test.ts` and `admin.test.ts` gain new test cases for
  the new methods/fields but don't need their existing cases rewritten.

## Open items for implementation plan

- Exact Drizzle migration for the two new `policies` columns.
- `conditionToSql`'s exact operator-to-SQL-function mapping (needs to be
  concrete in the plan).
- Whether `filterReadableFields`/`assertWritableFields` operate on the
  merged `{ ...topLevelColumns, ...data }` shape or on `data` alone — needs
  to be concrete; likely the same merged shape `CrudService.transition`
  already builds for its own subject, for consistency.
