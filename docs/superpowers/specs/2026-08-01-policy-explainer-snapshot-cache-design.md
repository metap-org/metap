# PolicyExplainer + PermissionSnapshotCache — Design

Date: 2026-08-01
Status: Approved, pending implementation plan

## Context

This is sub-project 4, the last of the "dynamic permission engine"
initiative:

1. Dynamic role assignment (shipped).
2. Policy storage + RBAC/ABAC evaluator (shipped).
3. Field-level + record-level enforcement (shipped, per
   `docs/superpowers/specs/2026-08-01-field-record-enforcement-design.md`).
4. **`PolicyExplainer` + `PermissionSnapshotCache` + tests** (this spec).

Sub-project 3 already fetches field/record policies once per `CrudService`
call and reuses them across every row/field check in that call — an
ad-hoc pattern repeated at each of the four call sites (`list`, `create`,
`update`, `transition`). This sub-project names and centralizes that
pattern as `PermissionSnapshotCache`, and adds `PolicyExplainer`, a
debug/simulation tool for answering "why was this decision made" — which,
because it needs to evaluate policies for an arbitrary hypothetical caller
(not just the requester's own token), also satisfies the roadmap's
original "policy simulator" goal for Phase 3.

## Goals

- `PermissionSnapshotCache`: a small type/loader that fetches all
  applicable policies for `(tenantId, entity)` once, and offers the same
  read/write/record-condition checks sub-project 3 built directly against
  raw policy rows — `CrudService`'s four call sites switch to building one
  snapshot at the top of each method and passing it through, instead of
  each making 2-3 separate `PermissionService` DB calls.
- `PolicyExplainer`: `explain(context, entity, action, options?)` → a trace
  of every policy considered and why it passed or failed, without actually
  enforcing anything (read-only introspection).
- A debug/simulation endpoint, `POST /admin/policies/explain`, admin-gated,
  that accepts an arbitrary hypothetical caller (`roles: string[]`, not the
  actual caller's token) plus `entity`/`action`/optional `field`/optional
  `record`, and returns the explain trace — this is the "policy simulator"
  from the roadmap.
- Consolidated test coverage across sub-projects 2-4's `PermissionService`
  surface.

## Non-goals

- Cross-request/TTL caching — `PermissionSnapshotCache` is a per-call
  batching helper, not a cache with a lifetime beyond one `CrudService`
  invocation. Naming it "cache" matches the roadmap's deliverable name;
  it is not a persistent cache.
- Exposing `explain` results to non-admin callers, or using it for
  anything other than debugging (it must never be used as an enforcement
  path itself — enforcement stays in sub-project 3's methods).

## Design

### 1. `PermissionSnapshotCache`

New file `src/core/permission/permission-snapshot.ts`:

```ts
export class PermissionSnapshot {
  private constructor(
    private readonly fieldPolicies: PolicyRow[],
    private readonly recordPoliciesByAction: Map<EntityAction, PolicyRow[]>,
  ) {}

  static async load(
    db: Database,
    tenantId: string,
    entity: string,
    actions: readonly EntityAction[],
  ): Promise<PermissionSnapshot>;

  filterReadableFields(context: RequestContext, record: Record<string, unknown>): Record<string, unknown>;
  assertWritableFields(context: RequestContext, payloadFields: readonly string[], existingRecord: Record<string, unknown> | undefined): PermissionDecision;
  canUpdateRecordCondition(context: RequestContext, record: Record<string, unknown>): PermissionDecision;
  recordReadCondition(): PolicyCondition | undefined; // OR of record-level "read" policy conditions, for QueryPlanner
}
```

This is a **refactor**, not new logic: the method bodies move (mostly
verbatim) from `PermissionService` (sub-project 3) into `PermissionSnapshot`,
which owns the already-fetched rows instead of re-querying per call.
`PermissionService` keeps `checkAction`/`can{Read,Create,Update}Entity`
(entity-level, unchanged) and gains one new method,
`loadSnapshot(tenantId, entity, actions)`, that constructs a
`PermissionSnapshot` via `PermissionSnapshot.load(this.db, ...)`.

`CrudService`'s four methods change from calling
`this.permissions.getFieldPolicies(...)` /
`this.permissions.getRecordPolicies(...)` separately to one
`const snapshot = await this.permissions.loadSnapshot(tenantId, entity.name, [...actions needed]);`
at the top, then using `snapshot.filterReadableFields(...)` etc. This is a
one-call-site-per-method mechanical change, not new business logic.

### 2. `PolicyExplainer`

New file `src/core/permission/policy-explainer.ts`:

```ts
export type PolicyTraceEntry = {
  policyId: string;
  roleGate: "open" | "passed" | "failed";
  conditionGate: "open" | "passed" | "failed";
  conditionReason?: string;
};

export type PolicyExplanation = {
  allowed: boolean;
  policiesConsidered: PolicyTraceEntry[];
};

export function explainPolicies(
  policyRows: PolicyRow[],
  context: RequestContext,
  subjectFor: (row: PolicyRow) => Record<string, unknown>,
): PolicyExplanation;
```

A pure function over already-fetched policy rows (doesn't itself query the
DB — callers fetch rows via `PermissionService`/`PermissionSnapshot` first,
then pass them in). For each row: role gate is `"open"` if `roles` is
null/empty, else `"passed"`/`"failed"` based on the caller's roles;
condition gate is `"open"` if `condition` is null, else the
`evaluateCondition` result mapped to `"passed"`/`"failed"` (with
`conditionReason` set on failure). `allowed` is `true` iff at least one row
has both gates non-`"failed"`.

`PermissionService.explain(context, entity, action, options?: { field?:
string; record?: Record<string, unknown> })` wraps this: fetches the
relevant policy rows (entity-level, or field-scoped if `field` given, or
record-scoped if the action implies it) and calls `explainPolicies`.

### 3. Debug endpoint

`POST /admin/policies/explain` in `src/server/routes/admin.ts`, admin-gated
like every other `/admin/*` route. Body: `{ roles: string[], entity:
string, action: "read" | "create" | "update", field?: string, record?:
Record<string, unknown> }`. Builds a synthetic `RequestContext` from the
given `roles` (plus the caller's own `tenantId` — always the caller's
tenant, never cross-tenant), calls `container.permissions.explain(...)`,
returns the trace as `{ data: PolicyExplanation }`.

### 4. Consolidated tests

`permission-service.test.ts` (already a live-DB suite from sub-project 2)
gains test cases for `loadSnapshot`, `explain`, and the
`/admin/policies/explain` endpoint gets coverage in `admin.test.ts` — no
new test files; both existing live-DB suites already have the
fixtures/setup this needs.

## Consequences for existing code

- `CrudService`'s four methods (sub-project 3's wiring) change their
  `PermissionService` call pattern from several direct calls to one
  `loadSnapshot` + snapshot method calls. Behavior is unchanged — this is
  a refactor sub-project 3's own code, done immediately after, not a
  change visible from outside `CrudService`.
- `QueryPlanner.planList`'s record-level-read parameter (sub-project 3)
  now comes from `snapshot.recordReadCondition()` instead of a raw
  `PolicyRow[]` — a small signature adjustment at its one call site.

## Open items for implementation plan

- Exact list of `actions` a `loadSnapshot` call needs per `CrudService`
  method (e.g. `list` needs `["read"]` record-level + all field policies
  regardless of action, since both read and write field policies must be
  fetched together for a single round trip).
- Whether `explain`'s policy-row fetch for record scope needs a `record`
  argument to be meaningful (yes — without one, condition gates default to
  evaluating against `{}`, same degenerate-but-documented behavior as
  sub-project 3's create-time field write check).
