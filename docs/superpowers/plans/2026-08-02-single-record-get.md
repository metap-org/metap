# Single-Record GET Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /api/:entity/:id` so a single record can be fetched, with the same entity-level, record-level, and field-level enforcement `list()` already has.

**Architecture:** `CrudService.get()` reuses the existing fetch-then-check shape from `update()`/`transition()` (SELECT by id/tenant/entity, 404 if missing) plus the existing `canUpdateRecordCondition(context, record, "read")` and `maskRecordForRead()` helpers — no new permission or masking mechanism. A new route in `src/server/routes/records.ts` wires it up.

**Tech Stack:** TypeScript, Fastify, Zod, Drizzle ORM, Vitest (live-DB tests).

## Global Constraints

- A record that exists but fails its record-level read policy returns `403 forbidden`, not `404` — matches `update()`/`transition()`'s existing convention for the same situation, not a new hide-existence convention.
- No new permission or masking logic — `canUpdateRecordCondition(context, record, "read")` and `maskRecordForRead()` already exist and are reused as-is.
- Minimal, targeted tests only — no exhaustive matrix, per this project's established test-scope convention.

---

### Task 1: `CrudService.get()` + tests

**Files:**
- Modify: `src/core/crud/crud-service.ts`
- Test: `src/core/crud/crud-service.test.ts`

**Interfaces:**
- Produces: `CrudService.get(entityName: string, id: string, context: RequestContext): Promise<ServiceResult<RecordDto>>` — consumed by Task 2's route.

- [ ] **Step 1: Write the failing tests**

In `src/core/crud/crud-service.test.ts`, inside the existing `describe("CrudService field/record enforcement (live DB)", ...)` block (uses `adminContext`/`editorContext`/`viewerContext`, all sharing `tenantId`), add these three tests right before the block's closing `});`:

```ts
  it("get() returns an existing record, field-masked the same way list() would mask it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "G001", name: "Get Enforcement Co", phone: "555-3000" },
        adminContext,
      );
      expect(created.ok).toBe(true);
      if (!created.ok) return;
      recordId = created.data.id;

      const result = await container.crud.get("crm.customers", recordId, viewerContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect((result.data.data as { phone?: string }).phone).toBeUndefined();
        expect((result.data.data as { name?: string }).name).toBe("Get Enforcement Co");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("get() returns 404 record_not_found for a non-existent id", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.get(
      "crm.customers",
      "00000000-0000-0000-0000-000000000099",
      adminContext,
    );

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.status).toBe(404);
      expect(result.error).toBe("record_not_found");
    }
  });

  it("get() returns 403 for a non-admin blocked by a record-level read policy, but still succeeds for admin", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "G002", name: "Get Record Policy Co" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const editorResult = await container.crud.get("crm.customers", created.data.id, editorContext);
      expect(editorResult.ok).toBe(false);
      if (!editorResult.ok) {
        expect(editorResult.status).toBe(403);
      }

      const adminResult = await container.crud.get("crm.customers", created.data.id, adminContext);
      expect(adminResult.ok).toBe(true);
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts -t "get()"`
Expected: FAIL — `container.crud.get` doesn't exist yet (`TypeError: container.crud.get is not a function`).

- [ ] **Step 3: Implement `CrudService.get()`**

In `src/core/crud/crud-service.ts`, add the new method right after `list()` (before `create()`):

```ts
  async get(
    entityName: string,
    id: string,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canReadEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const existingRows = await this.db.client
      .select()
      .from(records)
      .where(
        and(
          eq(records.id, id),
          eq(records.tenantId, context.tenantId),
          eq(records.entity, entity.name),
          eq(records.deleted, false),
        ),
      );

    const existing = existingRows[0];

    if (!existing) {
      return { ok: false, status: 404, error: "record_not_found" };
    }

    const existingData = existing.data as Record<string, unknown>;

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData, "read");

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    return {
      ok: true,
      data: this.maskRecordForRead(entity, context, snapshot, existing),
    };
  }
```

This mirrors `update()`'s/`transition()`'s existing fetch-then-check block exactly (same `and(eq(...))` shape, same 404 handling), the one difference being the third argument `"read"` passed to `canUpdateRecordCondition` instead of relying on its `"update"` default.

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: PASS — all tests in the file.

- [ ] **Step 5: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors (baseline: 17 pre-existing lint errors unrelated to this file).

- [ ] **Step 6: Commit**

```bash
git add src/core/crud/crud-service.ts src/core/crud/crud-service.test.ts
git commit -m "Add CrudService.get() for single-record reads with field/record-level enforcement"
```

---

### Task 2: `GET /api/:entity/:id` route

**Files:**
- Modify: `src/server/routes/records.ts`
- Test: `src/server/app.test.ts` (one thin route-level test, live-DB — the enforcement behavior itself is already covered by Task 1's `CrudService.get()` tests; this only proves the route is wired correctly)

**Interfaces:**
- Consumes: `container.crud.get(entity, id, context)` (Task 1).

- [ ] **Step 1: Write the failing test**

Check `src/server/app.test.ts`'s existing `describe("buildApp (record creation, live DB)", ...)` block (or the nearest equivalent live-DB route-level describe block that already creates a `crm.customers` record via the HTTP API) for its exact fixture pattern (how it builds the app, mints a token, and cleans up), then add one test there following that same pattern:

```ts
  it("GET /api/:entity/:id returns the record that was just created", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const tenantId = "00000000-0000-0000-0000-000000000010";
    const userId = "00000000-0000-0000-0000-000000000011";
    const token = jwt.sign({ tenantId, roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: userId,
      expiresIn: "1h",
    });

    let recordId: string | undefined;

    try {
      const createRes = await app.inject({
        method: "POST",
        url: "/api/crm.customers",
        headers: { authorization: `Bearer ${token}` },
        payload: { data: { code: "R001", name: "Route Get Co" } },
      });
      expect(createRes.statusCode).toBe(201);
      const created = createRes.json<{ data: { id: string } }>();
      recordId = created.data.id;

      const getRes = await app.inject({
        method: "GET",
        url: `/api/crm.customers/${created.data.id}`,
        headers: { authorization: `Bearer ${token}` },
      });

      expect(getRes.statusCode).toBe(200);
      expect(getRes.json<{ data: { id: string } }>().data.id).toBe(created.data.id);
    } finally {
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });
```

This matches the exact fixture pattern already used by this describe block's other tests (`privateKey`/`pgClient`/`dbAvailable` from `beforeAll`, a token minted per-test, `finally`-block cleanup) — verified against `src/server/app.test.ts`'s existing "persists the posted data payload intact" test.

- [ ] **Step 2: Run the test to verify it fails**

Run: `pnpm vitest run src/server/app.test.ts -t "GET /api/:entity/:id"`
Expected: FAIL — `404` (route doesn't exist yet, Fastify's default not-found handler).

- [ ] **Step 3: Implement the route**

In `src/server/routes/records.ts`, add `GetParamsSchema` near the existing `UpdateParamsSchema`:

```ts
const GetParamsSchema = z.object({ entity: z.string(), id: z.string().uuid() });
```

Add the route inside `registerRecordRoutes`, right after the existing `app.get("/api/:entity", ...)` list route and before the `app.post("/api/:entity", ...)` create route:

```ts
  app.get<{ Params: { entity: string; id: string } }>(
    "/api/:entity/:id",
    {
      schema: {
        params: z.toJSONSchema(GetParamsSchema, { target: "draft-7" }),
      },
    },
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

- [ ] **Step 4: Run the test to verify it passes**

Run: `pnpm vitest run src/server/app.test.ts`
Expected: PASS — all tests in the file.

- [ ] **Step 5: Typecheck, lint, full suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new lint errors (baseline 17); full suite passes (124 before this work + 3 from Task 1 + 1 from Task 2 = 128).

- [ ] **Step 6: Commit**

```bash
git add src/server/routes/records.ts src/server/app.test.ts
git commit -m "Add GET /api/:entity/:id route"
```
