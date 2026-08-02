# Policy Storage + RBAC/ABAC Evaluator — Design

Date: 2026-08-01
Status: Approved, pending implementation plan

## Context

This is sub-project 2 of the 4-part "dynamic permission engine" initiative
(roadmap Phase 3, built dynamic per explicit request rather than the
roadmap's more modest static RBAC+ABAC scaffold):

1. Dynamic role assignment (shipped — `docs/superpowers/specs/2026-07-31-dynamic-role-assignment-design.md`).
2. **Policy storage + RBAC/ABAC evaluator** (this spec).
3. Field-level + record-level enforcement — wiring the evaluator into
   `CrudService` and `QueryPlanner`.
4. `PolicyExplainer` + `PermissionSnapshotCache` + policy tests.

Today, `PermissionService.checkAction` (`src/core/permission/permission-service.ts`)
checks a caller's roles against `EntityDefinition.permissions` — a static
role allow-list declared in each `*.entity.ts` file. A grep across
`src/modules/` confirms **no entity currently declares `permissions`**, so
every entity is effectively wide open today (aside from the hardcoded
`admin` bypass). This sub-project moves policy definition out of code and
into the database, mirroring how sub-project 1 moved role assignment out
of the JWT.

## Goals

- Policies (which roles, and optionally which conditions, gate an entity
  action) are stored in Postgres, per tenant, and take effect immediately
  — no deploy needed to change who can do what.
- Support both RBAC (role allow-list) and ABAC (attribute condition) on
  the same policy, combinable.
- Multiple policies can apply to the same `(entity, action)`; any one
  passing is sufficient (OR semantics, allow-list only — no deny rules).
- The condition evaluator is built generically now (`subject` + `context`)
  so sub-project 3 can reuse it unchanged for record-level checks — it
  just passes record data as `subject` instead of `context`.
- A minimal admin API to create, list, and delete policies.

## Non-goals

- Field-level and record-level *enforcement* (masking fields, filtering
  list queries) — sub-project 3. This sub-project stores and evaluates
  policies for the three existing entity-level actions (`read`, `create`,
  `update`) only; the `policies` schema doesn't need a `scope`/`field`
  column yet because nothing consumes one.
- `PolicyExplainer` (a trace of why a decision was made) and
  `PermissionSnapshotCache` — sub-project 4.
- Policy update (PATCH) — delete and recreate, same simplicity tradeoff
  sub-project 1 made for role assignment.
- Deny rules / explicit negative policies. Absence of a matching policy
  still means "allowed" (matches today's default-open behavior for every
  entity with no declared restrictions) — adding a deny concept would be
  a bigger semantic change than requested here.
- A separate "auth service" or network boundary for policy evaluation.
  Per `docs/architectures/index.md`'s Multi-Service Evolution section,
  `PermissionService` already lives in `src/core`, the future
  `packages/core` — every eventual `apps/<module>` service imports it
  in-process, querying the same shared Postgres instance. `policies` is
  an infrastructure table (like `outbox_events`/`workflow_events`/
  `user_roles`), not entity business data, so it isn't subject to the
  "no cross-entity join" rule and needs no special handling for a future
  split.

## Design

### 1. Schema: `policies`

New Drizzle table in `src/infra/db/schema.ts`:

- `id` (uuid, pk)
- `tenantId` (uuid)
- `entity` (varchar(120)) — an `EntityDefinition.name`, e.g. `"crm.customers"`
- `action` (varchar(20)) — `"read"` | `"create"` | `"update"`
- `roles` (jsonb, nullable) — `string[]`; null/empty means this policy's
  RBAC gate is open (any authenticated caller passes it, evaluation moves
  to the condition gate)
- `condition` (jsonb, nullable) — a `PolicyCondition` tree; null means
  this policy's ABAC gate is open
- `createdAt` (timestamptz, default now)
- `createdBy` (uuid, nullable)

No unique constraint — multiple rows may share `(tenantId, entity,
action)` by design (OR semantics). Index on `(tenantId, entity, action)`,
the hot lookup path.

A policy with both `roles` and `condition` null is degenerate (always
passes) — the admin API doesn't forbid it, but it's pointless; not worth
validating against since it's harmless.

### 2. `PolicyCondition` — shared, generic evaluator

New file `src/core/permission/policy-condition.ts`:

```ts
export type PolicyValue = { literal: unknown } | { fromContext: keyof RequestContext };

export type PolicyCondition =
  | { attribute: string; op: "eq" | "neq" | "in" | "notIn"; value: PolicyValue }
  | { all: readonly PolicyCondition[] }
  | { any: readonly PolicyCondition[] };

export function evaluateCondition(
  condition: PolicyCondition,
  subject: Record<string, unknown>,
  context: RequestContext,
): true | string;
```

`attribute` is always resolved against `subject` — never directly against
`context`, even though `value` can pull from `context` via `fromContext`.
This asymmetry is deliberate: it's what lets the exact same function serve
two different callers without change:

- **This sub-project** (entity-level actions, no record in scope):
  `PermissionService` calls `evaluateCondition(condition, context as
  unknown as Record<string, unknown>, context)` — `subject` and `context`
  are the same object, so only context-attribute conditions are
  meaningful (e.g. `{ attribute: "functionId", op: "eq", value: { literal: "sales-app" } }`).
  Cross-referencing (`value: { fromContext: ... }`) is accepted but
  degenerates to comparing a context field against itself — harmless, not
  useful yet.
- **Sub-project 3** (record-level actions): will call
  `evaluateCondition(condition, recordData, context)` — now `value: {
  fromContext: "userId" }` becomes meaningful, e.g. `{ attribute:
  "createdBy", op: "eq", value: { fromContext: "userId" } }` for
  ownership scoping. No change to this function.

On failure, returns a string reason (e.g. `"condition failed on
attribute 'functionId'"`); on success, `true`.

### 3. `PermissionService` — async, DB-backed

`checkAction` (and therefore `canReadEntity`/`canCreateEntity`/
`canUpdateEntity`) becomes `async`:

1. `context.roles?.includes("admin")` → allow, unchanged (still a
   hardcoded bypass independent of the policy table — matches today's
   behavior, and avoids having to seed a policy row for every
   entity×action just to keep admins working).
2. Query `policies` for `(tenantId, entity, action)`.
3. No rows → allow (preserves today's default-open behavior for every
   entity that has no policies, including `crm.customers`).
4. Rows found → for each policy, in order: role gate passes if `roles` is
   null/empty or the caller has at least one listed role; condition gate
   passes if `condition` is null or `evaluateCondition(...)` returns
   `true`. A policy passes only if both gates pass. The action is allowed
   if **any** policy passes.
5. No policy passed → `{ allowed: false, reason: "forbidden" }`.

The constructor's `MetadataRegistry` parameter is dropped and replaced
with `Database`. `MetadataRegistry` was only ever used to read
`entity.permissions` (§5 deletes that field); it's not needed for
anything else in `checkAction`, and every caller already validates the
entity exists before invoking a permission check (`CrudService` looks up
the entity and 404s before calling `canReadEntity`/etc.), so
`PermissionService` doesn't need to re-resolve it. `container.ts`'s
`new PermissionService(metadata)` becomes `new PermissionService(db)`.

### 4. Admin API — `/admin/policies`

New routes in `src/server/routes/admin.ts` (alongside the existing
`/admin/users/*` routes from sub-project 1), same `isAdmin` gate:

- `GET /admin/policies?entity=crm.customers` — list policies for the
  tenant, optionally filtered by entity. `entity` omitted → all policies
  in the tenant.
- `POST /admin/policies` body `{ entity: string, action: "read" |
  "create" | "update", roles?: string[], condition?: PolicyCondition }` →
  creates a row, returns it.
- `DELETE /admin/policies/:id` → deletes by id (scoped to the caller's
  tenant — deleting another tenant's policy by guessing its id must
  fail, same as every other tenant-scoped delete in this codebase).

### 5. Delete the static `EntityPermissions` mechanism

`src/core/metadata/entity.ts` — remove `EntityPermissions` type and the
`permissions?: EntityPermissions` field from `EntityDefinition`. No
entity currently sets it (verified by grep), so this is a clean removal,
not a migration.

## Consequences for existing code

- `src/core/crud/crud-service.ts` has four call sites
  (`this.permissions.canReadEntity` / `canCreateEntity` /
  `canUpdateEntity` ×2) that currently call these methods without
  `await`. All four need `await` added now that the methods are async.
- `src/core/permission/permission-service.test.ts` tests the static
  `entity.permissions` mechanism directly and will not compile once
  `EntityPermissions` is deleted. It needs a full rewrite as a live-DB
  test suite (create policy rows, assert `checkAction` results),
  following the pattern established in
  `src/core/auth/role-assignment-service.test.ts`.
- `src/core/query/query-planner.ts` is unaffected — it only calls
  `permissions.scopedTenant(context)`, a pure synchronous helper unrelated
  to policy evaluation.

## Open items for implementation plan

- Exact Drizzle migration for `policies` (via `pnpm db:generate` /
  `db:migrate`).
- Whether `PermissionService`'s constructor should take `Database`
  directly or a lighter query wrapper — precedent from sub-project 1
  (`RoleAssignmentService`) is to take `Database` directly; follow that.
- `evaluateCondition`'s exact denial-reason string format — needs to be
  concrete in the plan, not just illustrative as in this spec.
