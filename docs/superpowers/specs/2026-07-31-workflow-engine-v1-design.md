# Workflow Engine V1 — Design

Date: 2026-07-31
Status: Approved, pending implementation plan

## Context

`WorkflowEngine` (`src/core/workflow/workflow-engine.ts`) currently only assigns
initial status on create and emits `<entity>.record.created` /
`<entity>.record.updated` outbox events. Entity metadata already declares
transitions (`EntityWorkflow.transitions`, e.g. `crm.customers`' `activate` /
`block`), but nothing executes them — `CrudService.update` explicitly freezes
the state field so it can never change through the generic update path.

This is roadmap Phase 5 ("Workflow Engine V1"): atomic transition, optimistic
locking, guard conditions, append-only workflow events, side effects through
the outbox. Notification integration (also listed in the roadmap) is scoped
down to a stub hook — an outbox topic a future notifier can subscribe to —
since no notification service exists yet to build the real thing against.

## Goals

- Execute a declared workflow transition (`action`) on a record, atomically,
  with optimistic locking via the existing `version` column.
- Support optional guard conditions that can block a transition beyond the
  from/to state match.
- Record every transition in a permanent, append-only audit log, separate
  from the transient `outbox_events` queue.
- Emit an outbox event on transition so a future notifier has something to
  subscribe to, without building that notifier now.

## Non-goals

- Field/record-level permission for transitions (Phase 3). Transitions reuse
  `PermissionService.canUpdateEntity` for now.
- A declarative/serializable guard DSL. Guards are TypeScript predicates
  defined in the entity file, consistent with how entities are already
  TS objects rather than JSON config.
- Building an actual notification consumer. Only the outbox topic is added.
- Changing the generic `PATCH /api/:entity/:id` behavior — it continues to
  freeze the state field; transitions get their own endpoint.

## Design

### 1. Metadata: guards on transitions

`src/core/metadata/entity.ts` — `WorkflowTransition` gains an optional guard:

```ts
export type WorkflowTransition = {
  action: string;
  from: string;
  to: string;
  label: string;
  guard?: (data: Record<string, unknown>, context: RequestContext) => true | string;
};
```

`RequestContext` is `import type`-ed from `../permission/permission-service`
— type-only import, so no runtime circularity even though `permission-service.ts`
also imports from the metadata module.

A guard returns `true` to allow the transition, or a string (the denial
reason, surfaced to the caller) to block it.

`crm.customers`' `activate` transition gets an example guard: block
activation if `email` is not set, so the feature is exercised by the one
entity that currently exists.

### 2. New table: `workflow_events`

Append-only audit log, distinct from `outbox_events` (a transient publish
queue whose rows get drained/marked-published). Added to
`src/infra/db/schema.ts`:

- `id` (uuid, pk)
- `tenantId` (uuid)
- `entity` (varchar)
- `recordId` (uuid)
- `action` (varchar)
- `fromState` (varchar)
- `toState` (varchar)
- `actor` (uuid, nullable — `context.userId`)
- `createdAt` (timestamptz, default now)

Index on `(tenantId, entity, recordId, createdAt)` for per-record history
lookups. Nothing ever updates or deletes rows in this table.

### 3. `WorkflowEngine` — extended, not split into a new class

Stays one class (one workflow boundary, per the existing fixed-boundary
convention) with new methods added alongside `getInitialStatus` /
`emitCreated` / `emitUpdated`:

- `findTransition(entity, action, fromState): WorkflowTransition | undefined`
- `runGuard(transition, data, context): true | string`
- `emitTransitioned(executor, entity, recordId, payload)` — outbox enqueue,
  topic `<entity>.workflow.transitioned`, payload
  `{ recordId, action, from, to, actor }`. This is the notification stub
  hook.
- `recordEvent(executor, entity, recordId, action, from, to, context)` —
  insert into `workflow_events`.

### 4. `CrudService.transition(entityName, id, action, expectedVersion, context)`

New method, same overall shape as `create`/`update`:

1. Entity lookup → 404 `entity_not_found`.
2. `permissions.canUpdateEntity(context, entity.name)` → 403 `forbidden`.
3. Fetch existing record (tenant + entity + id, `deleted = false`) → 404
   `record_not_found`.
4. `entity.workflow` missing → 400 `no_workflow`.
5. `currentState = existingData[entity.workflow.stateField]`.
6. `workflow.findTransition(entity, action, currentState)` — no match → 409
   `invalid_transition`. This single check covers three cases: unknown
   action, action not valid from the current state, and an attempted
   transition out of a terminal state (no transitions are defined with a
   terminal state as `from`).
7. `workflow.runGuard(transition, existingData, context)` returns a string →
   422 `guard_failed`, message = that string.
8. One DB transaction:
   - `UPDATE records SET data = data || {stateField: to}, status = to,
     version = version + 1, updatedAt = now(), updatedBy = context.userId
     WHERE id = :id AND tenantId = :tenantId AND entity = :entity AND
     version = :expectedVersion AND deleted = false` (same optimistic-lock
     pattern `update` already uses) — no row updated → 409
     `version_conflict`.
   - `workflow.recordEvent(...)` — insert `workflow_events` row.
   - `workflow.emitTransitioned(...)` — outbox enqueue.
9. Return the updated record.

### 5. Route

`POST /api/:entity/:id/transitions/:action` in
`src/server/routes/records.ts`, alongside the existing list/create/update
routes. Body: `{ version: number }` (zod-validated, same style as
`UpdateBodySchema`). Calls `container.crud.transition(...)`.

### 6. Error handling

`src/server/error-handler.ts` — add to `SERVICE_ERROR_MESSAGES`:

- `no_workflow`: "This entity has no workflow."
- `invalid_transition`: "This transition is not valid from the record's current state."
- `guard_failed`: uses the guard's returned reason as the message directly
  (not a fixed string), since the guard's whole purpose is a specific
  human-readable reason.

### 7. Testing

First vitest file in the repo. Per established preference, minimal and
targeted — not an exhaustive matrix. Covers `CrudService.transition`
branches directly:

- Happy path: valid transition succeeds, record updated, version
  incremented.
- Invalid transition: wrong action or wrong current state → 409.
- Guard failure: transition blocked, reason surfaced → 422.
- Version conflict: stale `expectedVersion` → 409.

## Open items for implementation plan

- Migration for `workflow_events` (via `pnpm db:generate` / `db:migrate`).
- Whether `workflow_events` insert and `workflow.recordEvent` need the
  `DbExecutor` type threaded the same way `emitCreated`/`emitUpdated`
  already take one (yes — same transaction as the record update).
- `CrudService.update` has a comment claiming the `status` column is "mirrored
  from data[stateField] only by `create`" and never recomputed elsewhere.
  Once `transition` also mirrors `status`, that comment becomes inaccurate
  and needs updating alongside this change.
