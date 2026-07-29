# PermissionService: Entity-Level RBAC

Date: 2026-07-29

Status: approved

Scope: fourth of four planned Phase 1 kernel pieces, in this priority order:

1. Auth + RequestContext + structured errors + request/trace id (done)
2. `CrudService` update + optimistic locking (done)
3. `QueryPlanner` hardening (done)
4. **`PermissionService` (entity-level RBAC)** (this spec)

Roadmap Phase 3 ("Permission Engine") lists a much larger set of eventual goals — RBAC + ABAC, field-level permission, record-level permission, policy context, a policy simulator, a permission snapshot cache. This spec deliberately covers only entity-level RBAC (can a role read/create/update a given entity at all), matching how the prior three items were each scoped to one coherent mechanism rather than the full roadmap phase. Field-level permission, record-level permission, a policy simulator, and caching are explicitly out of scope here and remain for later, separate passes.

## Motivation

`PermissionService`'s three methods (`canReadEntity`, `canCreateEntity`, `canUpdateEntity`) currently always return `{ allowed: true }` — a known Phase 0 placeholder documented in `CLAUDE.md`. This spec replaces that with a real, if minimal, RBAC check: whether the caller's role(s) are allowed to perform a given action on a given entity type at all.

## Design

### Metadata: `EntityDefinition.permissions`

Add an optional field to `EntityDefinition` (`src/core/metadata/entity.ts`):

```ts
export type EntityPermissions = {
  read?: readonly string[];
  create?: readonly string[];
  update?: readonly string[];
};
```

`EntityDefinition` gains `permissions?: EntityPermissions`. Each key lists the roles allowed to perform that action; an entity that doesn't declare `permissions` at all — like `crm.customers` today — has **no restriction**, matching current behavior exactly (no breaking change). This spec does not add a `permissions` block to `customer.entity.ts`; the test suite exercises the restriction path with its own throwaway test entity (see Testing below), not by changing real production entity config.

### `PermissionService`

Gains a `MetadataRegistry` constructor dependency, mirroring the pattern `QueryPlanner` already uses to look up entities by name — this means `CrudService`'s existing call sites (`this.permissions.canReadEntity(context, entity.name)`, etc.) don't change at all; only `container.ts`'s construction (`new PermissionService(metadata)`) does.

Decision logic, identical across all three actions:

1. If `context.roles` includes `"admin"`, allow unconditionally. `admin` is a hardcoded bypass role for this pass — not itself declared anywhere, not overridable per-entity. (A future, more careful distinction between an unconditional "root-admin" superuser and an ordinary "admin" role that still goes through real checks was discussed and deliberately deferred — not part of this spec.)
2. Look up the entity via `MetadataRegistry.getEntity(entityName)`. If it has no `permissions` block, or no entry for this specific action, allow (matches the "missing config = allow-all" default).
3. Otherwise, allow if the caller's `context.roles` (empty array if `undefined`) intersects the action's allowed-roles list at all; otherwise deny with `{ allowed: false, reason: "forbidden" }` — the existing string `CrudService` already falls back to when `decision.reason` is unset, so this is just making it explicit.

By the time `PermissionService` is asked about an entity, `CrudService` has already confirmed the entity is registered (its own `entity_not_found` check runs first) — so the metadata lookup inside `PermissionService` is not itself a new source of 404s.

### `scopedTenant`

Unchanged.

## Out of scope

- Field-level and record-level permission — still just entity-level.
- ABAC / policy context beyond role membership.
- A policy simulator/explainer.
- A permission snapshot cache (nothing here is expensive enough yet to need one — `MetadataRegistry` lookups are in-memory).
- A distinct "root-admin" superuser role separate from "admin" — deferred, noted above.
- Changing `customer.entity.ts` to actually declare a restrictive `permissions` block for real use — this spec proves the mechanism works via its own test fixture, not by changing production entity behavior.

## Testing (minimal — important cases only)

Nothing in this change touches the database — `PermissionService`'s logic is a pure in-memory lookup, and the denial path in `CrudService` returns before any DB call is made. Tests are pure unit tests against a hand-built `MetadataRegistry`/test entity, not live-DB integration tests like the prior three plans needed:

- `admin` role bypasses even an entity with a restrictive `permissions` block.
- An entity with no `permissions` declared allows any role.
- An entity with `permissions` declared allows a role that's in the list.
- An entity with `permissions` declared denies a role that's not in the list, with `{ allowed: false, reason: "forbidden" }`.

No exhaustive matrix beyond that, per project convention.
