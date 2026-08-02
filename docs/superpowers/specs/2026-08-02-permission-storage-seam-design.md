# Permission Storage Seam (`PolicyStore` interface)

Date: 2026-08-02

Status: approved

Scope: second of 4 sub-projects addressing DB-coupling risks found while reviewing `packages/core`'s architecture (sub-project 1: `docs/superpowers/specs/2026-08-02-outbox-row-locking-design.md`).

## Motivation

`PermissionService` and `PermissionSnapshot` both query the `policies` table directly through a raw Drizzle `Database`, wired identically to every other service in `createContainer`. Permission checks are request-time reads/gates that run *before* a business write, not co-committed with it — unlike the outbox, there's no transactional-atomicity reason `policies` has to live in the same Postgres database as `records`. This sub-project introduces the interface seam only — one implementation (Postgres), per the user's explicit choice — so a different backend (its own DB, a remote policy engine, OPA, whatever) becomes a later, contained change instead of a rewrite.

## Design

New file `packages/core/src/core/permission/policy-store.ts`:

```ts
export interface PolicyStore {
  findContextPolicies(tenantId: string, entity: string, action: string): Promise<PolicyRow[]>;
  loadAllPolicies(tenantId: string, entity: string): Promise<PolicyRow[]>;
  findExplainPolicies(
    tenantId: string,
    entity: string,
    action: string,
    options?: { field?: string; subject?: "context" | "record" },
  ): Promise<PolicyRow[]>;
  listPolicies(tenantId: string, entity?: string): Promise<PolicyRow[]>;
  createPolicy(
    tenantId: string,
    entity: string,
    action: string,
    roles: string[] | undefined,
    condition: PolicyCondition | undefined,
    createdBy: string | undefined,
    field?: string,
    subject?: "context" | "record",
  ): Promise<PolicyRow>;
  deletePolicy(tenantId: string, id: string): Promise<void>;
}
```

Each method mirrors one of `PermissionService`/`PermissionSnapshot`'s current direct queries exactly (same `WHERE` conditions, same shape) — this is a pure seam, not a behavior change. `PostgresPolicyStore implements PolicyStore` moves the current Drizzle query bodies verbatim from `PermissionService`/`PermissionSnapshot.load` into this one class.

`PolicyRow` (currently `typeof policies.$inferSelect`, defined in and exported from `permission-service.ts`) moves to being defined in `policy-store.ts` instead — the natural home once there's a dedicated data-access module — with `permission-service.ts` re-exporting it (`export type { PolicyRow } from "./policy-store"`) so the 5 existing files that import it from `./permission-service` (`policy-explainer.ts`, `policy-condition.ts`, `condition-to-sql.ts`, `query-planner.ts`, `policy-explainer.test.ts`) need no changes.

`PermissionService`'s constructor changes from `constructor(private readonly db: Database)` to `constructor(private readonly store: PolicyStore)`; every method body swaps its direct Drizzle query for the matching `this.store.*` call. `PermissionSnapshot.load(db, tenantId, entity)` becomes `PermissionSnapshot.load(store: PolicyStore, tenantId, entity)`, calling `store.loadAllPolicies(tenantId, entity)` instead of querying `policies` directly. `PermissionService.loadSnapshot` (which currently does `PermissionSnapshot.load(this.db, tenantId, entity)`) updates to pass `this.store`.

`createContainer` (`container.ts`) constructs `const policyStore = new PostgresPolicyStore(db);` and passes `policyStore` to `new PermissionService(policyStore)` instead of `db`.

**Public API unchanged.** Every `PermissionService` method (`canReadEntity`, `canCreateEntity`, `canUpdateEntity`, `canDeleteEntity`, `loadSnapshot`, `listPolicies`, `createPolicy`, `deletePolicy`, `explain`) keeps its exact existing signature — only the constructor and internal query calls change. Every existing test that goes through `container.permissions.*` (the overwhelming majority of the permission-related test suite) needs zero changes, since it only ever calls the public API, never reaches into how `PermissionService` gets its data.

## Testing

TDD:
- New unit test proving the seam actually exists and is used (not just cosmetic): construct `PermissionService` with a hand-written mock `PolicyStore` (no live DB), call `canReadEntity`, assert the mock's `findContextPolicies` was called with the right arguments and the result reflects the mock's return value. This test cannot pass against today's `PermissionService(db: Database)` constructor — writing it first is what drives the interface into existence.
- Full existing live-DB suite (`permission-service.test.ts`, `permission-snapshot.test.ts`, `policy-explainer.test.ts`, `crud-service.test.ts`, `query-planner.test.ts`, `admin.test.ts`) must pass completely unchanged — this is the regression proof that the refactor preserves behavior exactly.

## Out of scope

- A second real `PolicyStore` implementation (OPA, a different DB, etc.) — this sub-project only introduces the seam.
- Sub-projects 3 (outbox per-service DB configurability) and 4 (config-drift enforcement) — separate, sequential work.
