# Single-Record GET Endpoint

Date: 2026-08-02

Status: approved

## Motivation

`CrudService` has `list`/`create`/`update`/`transition` but no way to fetch one record by id — `GET /api/:entity` (list) is the only read path. This means record-level read policies (`PermissionSnapshot.canUpdateRecordCondition`, evaluated with `action: "read"`) have never actually been exercised outside of `list()`'s `WHERE`-clause form, and any future frontend "record detail" screen has no endpoint to call. Documented as an open gap in `docs/roadmap.md`'s Phase 3 write-up and `docs/architectures/11-risks.md`: "Record-level read enforcement only runs through `list()` — there's no single-record `GET /api/:entity/:id` endpoint yet for it to cover."

## Design

### `CrudService.get(entityName, id, context)`

New method, following the exact fetch-then-check shape `update()`/`transition()` already use:

1. Look up the entity (`this.metadata.getEntity`) — `404 entity_not_found` if missing, same as every other method.
2. `this.permissions.canReadEntity(context, entity.name)` — entity-level read check, same call `list()` already makes. `403` on denial.
3. `SELECT` the row by `id + tenantId + entity + deleted = false` (identical `WHERE` to `update()`/`transition()`'s existing-row lookup). `404 record_not_found` if no row.
4. `snapshot.canUpdateRecordCondition(context, existingData, "read")` — reuses the existing method with its existing `action` parameter, just passing `"read"` instead of relying on the `"update"` default. Already has the admin bypass (fixed earlier this session) and evaluates record-level read policies (`getRecordPolicies("read")` internally) against this specific row. **`403 forbidden` on denial** — not `404` — deliberately matching `update()`/`transition()`'s existing behavior for a record that exists but fails its record-level condition check, rather than introducing a second convention (hide-vs-reveal existence) in the same service.
5. `this.maskRecordForRead(entity, context, snapshot, existing)` — the exact helper `list()`/`create()`/`update()`/`transition()` already use for field-level masking (including the `code`/`status` mirror-column masking fixed earlier this session).
6. Return `{ ok: true, data: RecordDto }` — same shape as `create()`/`update()`/`transition()`.

No new permission mechanism, no new masking mechanism — this wires two already-correct, already-tested pieces (`canUpdateRecordCondition`, `maskRecordForRead`) into a fetch path that happens not to exist yet.

### Route: `GET /api/:entity/:id`

Added to `src/server/routes/records.ts`, alongside the existing `PATCH /api/:entity/:id`:

```ts
app.get<{ Params: { entity: string; id: string } }>(
  "/api/:entity/:id",
  { schema: { params: z.toJSONSchema(GetParamsSchema, { target: "draft-7" }) } },
  async (request, reply) => {
    const params = GetParamsSchema.parse(request.params);
    const result = await container.crud.get(params.entity, params.id, request.context);

    if (!result.ok) {
      return sendServiceError(request, reply, result);
    }

    return { data: result.data };
  },
);
```

`GetParamsSchema = z.object({ entity: z.string(), id: z.string().uuid() })` — identical shape to the existing `UpdateParamsSchema`, just without the body.

## Out of scope (deliberate, not an oversight)

- **Field-level `read` masking already applies via `maskRecordForRead`** — no new work needed, but explicitly noting it's in scope of *this* endpoint's behavior, not a separate follow-up.
- **No `ETag`/conditional-GET support.** The response already carries `version` in the body for optimistic locking on a subsequent `update()`; HTTP-level caching headers are a separate, unrequested concern.
- **No expansion/embedding of related records.** Returns exactly the one record, same shape as every other `CrudService` method — no `?include=` mechanism.

## Testing (minimal — important cases only)

- One test: fetching an existing record returns it, masked the same way `list()` would mask it (reuse the field-masking policy setup already established in `crud-service.test.ts`'s "field/record enforcement" describe block).
- One test: fetching a non-existent id returns `404 record_not_found`.
- One test: fetching a record that fails a record-level read policy (`action: "read"`, non-admin caller) returns `403 forbidden` — and, separately, an **admin** caller with the same policy in place still succeeds (regression-style check mirroring the `list()` admin-bypass fix from earlier this session, now covering the single-record path too).

No exhaustive matrix beyond that, per project convention.
