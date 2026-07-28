# CrudService.update + Optimistic Locking

Date: 2026-07-28

Status: approved

Scope: second of four planned Phase 1 kernel pieces, in this priority order:

1. Auth + RequestContext + structured errors + request/trace id (done)
2. **`CrudService` update + optimistic locking** (this spec)
3. `QueryPlanner` hardening (metadata-constrained filters, tenant scope, max limit)
4. `PermissionService` (real RBAC/ABAC)

## Motivation

`CrudService` currently only has `list` and `create` — there is no way to update a record at all. The `records` table already has a `version` column (`src/infra/db/schema.ts`) reserved for optimistic locking, but nothing reads or writes it. Two concurrent updates to the same record would silently clobber each other with no way to detect the conflict.

## Design

### Route

`PATCH /api/:entity/:id`, inside the same protected Fastify plugin scope as the existing record routes (auth already required there).

Request body: `{ version: number, data: Record<string, unknown> }`.

### `CrudService.update(entityName, id, expectedVersion, rawData, context)`

1. `entity = metadata.getEntity(entityName)` — not found → `{ ok: false, status: 404, error: "entity_not_found" }`.
2. `permissions.canUpdateEntity(context, entity.name)` — not allowed → `403 forbidden`.
3. Fetch the existing record scoped by `tenantId` + `entity` + `id` + `deleted = false`. Not found → `404 record_not_found` (distinct from `entity_not_found`, which means the entity *type* isn't registered at all).
4. Merge: `mergedData = { ...record.data, ...rawData }` — partial merge, only the fields the client sent overwrite existing ones.
5. **Block state-field changes through this path**: if `entity.workflow` is defined, force `mergedData[entity.workflow.stateField] = record.data[entity.workflow.stateField]` regardless of what the client sent. State transitions are reserved for the future workflow-transition endpoint (roadmap Phase 5); allowing them through generic update would let a caller bypass workflow rules once those rules actually exist.
6. Validate `mergedData` against `entity.schema.safeParse(...)` (full validation of the merged result, same as `create`) — fails → `400 validation_failed`.
7. Recompute the mirrored `code` column the same way `create` does: `typeof mergedData.code === "string" ? mergedData.code : null`.
8. Atomic conditional update — this single statement *is* the optimistic lock, no wrapping transaction needed:
   ```ts
   const updated = await db.client
     .update(records)
     .set({ data: mergedData, code, version: sql`${records.version} + 1`, updatedAt: new Date(), updatedBy: context.userId })
     .where(and(eq(records.id, id), eq(records.tenantId, context.tenantId), eq(records.version, expectedVersion), eq(records.deleted, false)))
     .returning();
   ```
   Zero rows returned → the record's version no longer matches what the client last read → `409 version_conflict`.
9. On success, emit `<entity>.record.updated` through the outbox (mirrors the existing `emitCreated` → `<entity>.record.created` pattern in `WorkflowEngine`, for consistency — add a small `emitUpdated` alongside it).

### `PermissionService.canUpdateEntity`

New stub method, same shape as the existing `canReadEntity`/`canCreateEntity` — allows everything for now. Real enforcement is item 4 in the priority list.

### Error vocabulary additions (`src/server/error-handler.ts`)

Add to `SERVICE_ERROR_MESSAGES`:
- `record_not_found` → `"Record not found."`
- `version_conflict` → `"The record was modified by someone else. Reload and try again."`

`sendServiceError` already maps `result.status` straight through, so `409` for `version_conflict` and `404` for `record_not_found` need no changes there — just the message-lookup table.

## Out of scope

- `GET /api/:entity/:id` (fetch a single record by id) — not added. The client already has the current `version` from the `create`/`list`/`update` response bodies; a dedicated get-by-id endpoint isn't required for optimistic locking to work or be tested end-to-end. Can be added later if a real client needs it.
- Real workflow transition logic (roadmap Phase 5) — this spec only *blocks* state-field changes through generic update; it doesn't build the transition endpoint itself.
- Real RBAC in `canUpdateEntity` — still a stub, per the priority order.

## Testing (minimal — important cases only)

- One test for the happy path: create a record, update it with the correct version, assert the merge worked and version incremented.
- One test for the conflict path: create a record, update it once (version now 2), then attempt another update using the stale version (1) → assert `409 version_conflict`.
- One test confirming a state-field change in the update body is silently ignored (record's state stays what it was).

No exhaustive matrix beyond that, per project convention.
