# PolicyExplainer + PermissionSnapshotCache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate sub-project 3's per-call policy fetching (2 separate DB queries per `CrudService` method) into a single `PermissionSnapshot` load, and add `PolicyExplainer` — a read-only decision-trace tool exposed via an admin debug/simulation endpoint (the roadmap's "policy simulator").

**Architecture:** New `PermissionSnapshot` class owns policy rows already fetched in one query and exposes the same field/record checks sub-project 3 built directly on `PermissionService` — those methods move (not duplicate) into the snapshot. `PermissionService` gains `loadSnapshot` (replacing the removed direct methods) and `explain`. A new pure `explainPolicies` function in `policy-explainer.ts` produces the trace; `PermissionService.explain` fetches the relevant rows and calls it.

**Tech Stack:** Fastify, Zod, Drizzle ORM, PostgreSQL, vitest (live-DB integration tests for anything touching the DB; plain unit tests for the pure `explainPolicies` function).

## Global Constraints

- Specs: `docs/superpowers/specs/2026-08-01-policy-explainer-snapshot-cache-design.md`.
- This is the last sub-project of the "dynamic permission engine" initiative.
- `PermissionSnapshot` is a **per-`CrudService`-call** batching helper, not cross-request/TTL cache — explicitly decided against caching beyond one call.
- Two known bugs from sub-project 3 (admin bypass missing in `recordPolicyWhereClause`'s SQL path; field masking not covering top-level `code`/`status` columns) are **out of scope for this plan** — already recorded for a separate bugfix plan. Do not fix them here; note if this plan's refactor touches the same code paths (it does, in Task 4), but the actual fix stays deferred.
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for review.
- `docker compose up -d postgres rabbitmq` must be running for any live-DB test.
- Run `pnpm typecheck` after any type-level change. Pre-existing errors in `src/infra/messaging/rabbitmq.ts` are known and unrelated.

### Deviations from the spec, resolved during planning

- `PermissionSnapshot.load` takes `(db, tenantId, entity)` — **no `actions` parameter**, unlike the spec's sketch. It fetches every policy row for `(tenantId, entity)` in one query and partitions client-side (field-scoped vs. record-scoped-by-action), rather than trying to filter by a list of actions in SQL. This is simpler and is what actually gets sub-project 3's 2-queries-per-call down to 1.
- `recordReadCondition(): PolicyCondition | undefined` (the spec's sketch) is **not built** — it had no `context` parameter, but role-gate filtering needs one. Instead, `PermissionSnapshot.getRecordPolicies(action)` returns raw `PolicyRow[]`, and `QueryPlanner`'s existing `recordPolicyWhereClause(rows, context)` (built in sub-project 3, already living in `query/condition-to-sql.ts` — the one place SQL gets built, per the CLAUDE.md boundary) keeps doing the role-filter-then-OR-to-SQL step unchanged. This avoids duplicating that logic in two files.
- The `evaluatePolicyRow` helper (private to `PermissionService` in sub-project 3) is extracted to a plain exported function in `policy-condition.ts`, since `PermissionSnapshot` needs the identical logic and the two classes don't share a base type.

---

### Task 1: Extract `evaluatePolicyRow` as a shared function

**Files:**
- Modify: `src/core/permission/policy-condition.ts`
- Modify: `src/core/permission/permission-service.ts` (only `checkAction`'s use of it, not its full rewrite yet — that's Task 3)

**Interfaces:**
- Produces: `evaluatePolicyRow(policy: PolicyRow, context: RequestContext, recordSubject: Record<string, unknown> | undefined): boolean` — consumed by `checkAction` (this task) and Task 2's `PermissionSnapshot`.

- [ ] **Step 1: Add `evaluatePolicyRow` to `policy-condition.ts`**

In `src/core/permission/policy-condition.ts`, add this import at the top:

```ts
import type { PolicyRow, RequestContext } from "./permission-service";
```

(replacing the existing `import type { RequestContext } from "./permission-service";` line — this is a type-only circular import, same pattern already used between `entity.ts` and `permission-service.ts`, safe because `import type` is erased at compile time.)

Then add this function, right after `roleGatePassed`:

```ts
export function evaluatePolicyRow(
  policy: PolicyRow,
  context: RequestContext,
  recordSubject: Record<string, unknown> | undefined,
): boolean {
  if (!roleGatePassed(policy.roles as string[] | null, context.roles)) {
    return false;
  }

  const condition = policy.condition as PolicyCondition | null;

  if (!condition) {
    return true;
  }

  const subject = policy.subject === "record" && recordSubject ? recordSubject : context;
  return evaluateCondition(condition, subject, context) === true;
}
```

- [ ] **Step 2: Use it from `checkAction`, remove the private copy**

In `src/core/permission/permission-service.ts`, change the import line:

```ts
import { evaluateCondition, roleGatePassed } from "./policy-condition";
```

to:

```ts
import { evaluatePolicyRow } from "./policy-condition";
```

Delete the private `evaluatePolicyRow` method (lines 26-43 in the current file — the whole `private evaluatePolicyRow(...) { ... }` block). Change `checkAction`'s line:

```ts
    const passed = rows.some((policy) => this.evaluatePolicyRow(context, policy, undefined));
```

to:

```ts
    const passed = rows.some((policy) => evaluatePolicyRow(policy, context, undefined));
```

Every other usage of `this.evaluatePolicyRow(...)` in this file (inside `filterReadableFields`, `assertWritableFields`, `canUpdateRecordCondition`) stays as `this.evaluatePolicyRow(...)` for now — **do not touch those in this task**, they're deleted wholesale in Task 3 when those methods move to `PermissionSnapshot`. This task only touches `checkAction`.

- [ ] **Step 3: Run the existing tests to verify nothing broke**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts src/core/permission/policy-condition.test.ts`
Expected: PASS (all existing tests — this is a pure refactor of `checkAction`'s internals, no behavior change).

- [ ] **Step 4: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 2: `PermissionSnapshot`

**Files:**
- Create: `src/core/permission/permission-snapshot.ts`
- Create: `src/core/permission/permission-snapshot.test.ts`

**Interfaces:**
- Consumes: `evaluatePolicyRow` (Task 1), `PolicyRow`/`EntityAction`/`PermissionDecision`/`RequestContext` (from `permission-service.ts`, unchanged), `Database`.
- Produces (consumed by Task 3's `PermissionService.loadSnapshot` and Task 4's `CrudService`):
  - `PermissionSnapshot.load(db, tenantId, entity): Promise<PermissionSnapshot>`
  - `getRecordPolicies(action: EntityAction): PolicyRow[]`
  - `filterReadableFields(context, record): Record<string, unknown>`
  - `assertWritableFields(context, payloadFields, existingRecord): PermissionDecision`
  - `canUpdateRecordCondition(context, record, action?: EntityAction): PermissionDecision`

- [ ] **Step 1: Write the failing tests**

Create `src/core/permission/permission-snapshot.test.ts`:

```ts
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { PermissionSnapshot } from "./permission-snapshot";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";

const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";

describe("PermissionSnapshot (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let service: PermissionService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000080";
  const entity = "test.snapshot";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new PermissionService(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping PermissionSnapshot live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.end();
    }
    await db.close();
  });

  async function cleanup() {
    await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
  }

  function contextWithRoles(roles: string[], extra?: Partial<RequestContext>): RequestContext {
    return { tenantId, roles, ...extra };
  }

  it("filterReadableFields strips a field the caller cannot read", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "read", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);
      const record = { name: "Alice", salary: 100000 };

      const asHr = snapshot.filterReadableFields(contextWithRoles(["hr"]), record);
      expect(asHr).toEqual({ name: "Alice", salary: 100000 });

      const asViewer = snapshot.filterReadableFields(contextWithRoles(["viewer"]), record);
      expect(asViewer).toEqual({ name: "Alice" });
    } finally {
      await cleanup();
    }
  });

  it("filterReadableFields evaluates a record-subject condition per field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        undefined,
        { attribute: "status", op: "eq", value: { literal: "active" } },
        undefined,
        "internalNotes",
        "record",
      );

      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);
      const activeRecord = { status: "active", internalNotes: "secret" };
      const draftRecord = { status: "draft", internalNotes: "secret" };

      expect(snapshot.filterReadableFields(contextWithRoles(["viewer"]), activeRecord)).toEqual(
        activeRecord,
      );
      expect(snapshot.filterReadableFields(contextWithRoles(["viewer"]), draftRecord)).toEqual({
        status: "draft",
      });
    } finally {
      await cleanup();
    }
  });

  it("assertWritableFields rejects a payload touching a field the caller cannot write", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "write", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);

      const allowed = snapshot.assertWritableFields(
        contextWithRoles(["hr"]),
        ["name", "salary"],
        undefined,
      );
      expect(allowed.allowed).toBe(true);

      const denied = snapshot.assertWritableFields(
        contextWithRoles(["viewer"]),
        ["name", "salary"],
        undefined,
      );
      expect(denied.allowed).toBe(false);
      expect(denied.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("canUpdateRecordCondition evaluates against the record, not context, defaulting to the 'update' action", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        undefined,
        { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
        undefined,
        undefined,
        "record",
      );

      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);
      const callerContext = contextWithRoles(["editor"], { userId: "user-1" });

      const owned = snapshot.canUpdateRecordCondition(callerContext, { createdBy: "user-1" });
      expect(owned.allowed).toBe(true);

      const notOwned = snapshot.canUpdateRecordCondition(callerContext, {
        createdBy: "someone-else",
      });
      expect(notOwned.allowed).toBe(false);
    } finally {
      await cleanup();
    }
  });

  it("getRecordPolicies returns only rows matching the requested action", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        undefined,
        undefined,
        undefined,
        undefined,
        "record",
      );
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        undefined,
        undefined,
        undefined,
        undefined,
        "record",
      );

      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);
      expect(snapshot.getRecordPolicies("read")).toHaveLength(1);
      expect(snapshot.getRecordPolicies("update")).toHaveLength(1);
      expect(snapshot.getRecordPolicies("create")).toHaveLength(0);
    } finally {
      await cleanup();
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/permission-snapshot.test.ts`
Expected: FAIL — the module `./permission-snapshot` does not exist.

- [ ] **Step 3: Implement `PermissionSnapshot`**

Create `src/core/permission/permission-snapshot.ts`:

```ts
import { and, eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluatePolicyRow } from "./policy-condition";
import type { EntityAction, PermissionDecision, PolicyRow, RequestContext } from "./permission-service";

export class PermissionSnapshot {
  private constructor(
    private readonly fieldPolicies: PolicyRow[],
    private readonly recordPoliciesByAction: Map<string, PolicyRow[]>,
  ) {}

  static async load(db: Database, tenantId: string, entity: string): Promise<PermissionSnapshot> {
    const rows = await db.client
      .select()
      .from(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.entity, entity)));

    const fieldPolicies = rows.filter((row) => row.field !== null);

    const recordPoliciesByAction = new Map<string, PolicyRow[]>();
    for (const row of rows) {
      if (row.field === null && row.subject === "record") {
        const list = recordPoliciesByAction.get(row.action) ?? [];
        list.push(row);
        recordPoliciesByAction.set(row.action, list);
      }
    }

    return new PermissionSnapshot(fieldPolicies, recordPoliciesByAction);
  }

  getRecordPolicies(action: EntityAction): PolicyRow[] {
    return this.recordPoliciesByAction.get(action) ?? [];
  }

  filterReadableFields(
    context: RequestContext,
    record: Record<string, unknown>,
  ): Record<string, unknown> {
    if (context.roles?.includes("admin")) {
      return record;
    }

    const readPoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of this.fieldPolicies) {
      if (policy.action !== "read" || !policy.field) {
        continue;
      }
      const list = readPoliciesByField.get(policy.field) ?? [];
      list.push(policy);
      readPoliciesByField.set(policy.field, list);
    }

    const result: Record<string, unknown> = {};

    for (const [key, value] of Object.entries(record)) {
      const fieldReadPolicies = readPoliciesByField.get(key);

      if (!fieldReadPolicies || fieldReadPolicies.length === 0) {
        result[key] = value;
        continue;
      }

      const passed = fieldReadPolicies.some((policy) => evaluatePolicyRow(policy, context, record));

      if (passed) {
        result[key] = value;
      }
    }

    return result;
  }

  assertWritableFields(
    context: RequestContext,
    payloadFields: readonly string[],
    existingRecord: Record<string, unknown> | undefined,
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const writePoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of this.fieldPolicies) {
      if (policy.action !== "write" || !policy.field) {
        continue;
      }
      const list = writePoliciesByField.get(policy.field) ?? [];
      list.push(policy);
      writePoliciesByField.set(policy.field, list);
    }

    for (const field of payloadFields) {
      const fieldWritePolicies = writePoliciesByField.get(field);

      if (!fieldWritePolicies || fieldWritePolicies.length === 0) {
        continue;
      }

      const passed = fieldWritePolicies.some((policy) =>
        evaluatePolicyRow(policy, context, existingRecord),
      );

      if (!passed) {
        return { allowed: false, reason: "forbidden" };
      }
    }

    return { allowed: true };
  }

  canUpdateRecordCondition(
    context: RequestContext,
    record: Record<string, unknown>,
    action: EntityAction = "update",
  ): PermissionDecision {
    const recordPolicies = this.getRecordPolicies(action);

    if (context.roles?.includes("admin") || recordPolicies.length === 0) {
      return { allowed: true };
    }

    const passed = recordPolicies.some((policy) => evaluatePolicyRow(policy, context, record));

    return passed ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/permission-snapshot.test.ts`
Expected: PASS (5 tests).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 3: `PermissionService.loadSnapshot`; remove the superseded methods

**Files:**
- Modify: `src/core/permission/permission-service.ts` (full file)
- Modify: `src/core/permission/permission-service.test.ts` (remove 4 tests, moved to Task 2's new file)

**Interfaces:**
- Consumes: `PermissionSnapshot` (Task 2).
- Produces: `PermissionService.loadSnapshot(tenantId, entity): Promise<PermissionSnapshot>`.
- Removes: `getFieldPolicies`, `getRecordPolicies`, `filterReadableFields`, `assertWritableFields`, `canUpdateRecordCondition` (all superseded by `PermissionSnapshot`).

- [ ] **Step 1: Remove the 4 now-duplicated tests from `permission-service.test.ts`**

In `src/core/permission/permission-service.test.ts`, delete these four `it(...)` blocks in their entirety (they now live in `permission-snapshot.test.ts`, Task 2):

- `"filterReadableFields strips a field the caller cannot read"`
- `"filterReadableFields evaluates a record-subject condition per field"`
- `"assertWritableFields rejects a payload touching a field the caller cannot write"`
- `"canUpdateRecordCondition evaluates against the record, not context"`

Leave `"checkAction ignores field-scoped and record-scoped policies when checking entity-level actions"` — it only calls `createPolicy`/`canReadEntity`, unaffected by this refactor.

- [ ] **Step 2: Replace the full contents of `permission-service.ts`**

```ts
import { and, eq, isNull } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluatePolicyRow } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";
import { explainPolicies } from "./policy-explainer";
import type { PolicyExplanation } from "./policy-explainer";
import { PermissionSnapshot } from "./permission-snapshot";

export type PolicyRow = typeof policies.$inferSelect;

export type RequestContext = {
  tenantId: string;
  userId?: string;
  roles?: readonly string[];
  functionId?: string;
};

export type PermissionDecision = {
  allowed: boolean;
  reason?: string;
};

export type EntityAction = "read" | "create" | "update";

export class PermissionService {
  constructor(private readonly db: Database) {}

  private async checkAction(
    context: RequestContext,
    entityName: string,
    action: EntityAction,
  ): Promise<PermissionDecision> {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const rows = await this.db.client
      .select()
      .from(policies)
      .where(
        and(
          eq(policies.tenantId, context.tenantId),
          eq(policies.entity, entityName),
          eq(policies.action, action),
          isNull(policies.field),
          eq(policies.subject, "context"),
        ),
      );

    if (rows.length === 0) {
      return { allowed: true };
    }

    const passed = rows.some((policy) => evaluatePolicyRow(policy, context, undefined));

    return passed ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }

  canReadEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "read");
  }

  canCreateEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "create");
  }

  canUpdateEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "update");
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }

  async loadSnapshot(tenantId: string, entity: string): Promise<PermissionSnapshot> {
    return PermissionSnapshot.load(this.db, tenantId, entity);
  }

  async listPolicies(tenantId: string, entity?: string) {
    const where = entity
      ? and(eq(policies.tenantId, tenantId), eq(policies.entity, entity))
      : eq(policies.tenantId, tenantId);

    return this.db.client.select().from(policies).where(where);
  }

  async createPolicy(
    tenantId: string,
    entity: string,
    action: string,
    roles: string[] | undefined,
    condition: PolicyCondition | undefined,
    createdBy: string | undefined,
    field?: string,
    subject?: "context" | "record",
  ) {
    const inserted = await this.db.client
      .insert(policies)
      .values({
        tenantId,
        entity,
        action,
        roles: roles ?? null,
        condition: condition ?? null,
        createdBy,
        field: field ?? null,
        subject: subject ?? "context",
      })
      .returning();

    return inserted[0];
  }

  async deletePolicy(tenantId: string, id: string): Promise<void> {
    await this.db.client
      .delete(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.id, id)));
  }

  async explain(
    context: RequestContext,
    entity: string,
    action: string,
    options?: { field?: string; record?: Record<string, unknown> },
  ): Promise<PolicyExplanation> {
    const base = [
      eq(policies.tenantId, context.tenantId),
      eq(policies.entity, entity),
      eq(policies.action, action),
    ];

    const where = options?.field
      ? and(...base, eq(policies.field, options.field))
      : options?.record
        ? and(...base, isNull(policies.field), eq(policies.subject, "record"))
        : and(...base, isNull(policies.field), eq(policies.subject, "context"));

    const rows = await this.db.client.select().from(policies).where(where);

    return explainPolicies(rows, context, options?.record);
  }
}
```

Note: this task's rewrite adds the `explain` method too (Task 6 in the original decomposition would otherwise need to re-open this same file for one method — folding it in now avoids a second full-file replace; Task 6 below only adds `policy-explainer.ts` itself and `explain`'s tests, not another edit to this file).

- [ ] **Step 3: Typecheck — confirm the expected breakage**

Run: `pnpm typecheck`
Expected: errors in `src/core/crud/crud-service.ts` (still calling the now-removed `getFieldPolicies`/`getRecordPolicies`/`filterReadableFields`/`assertWritableFields`/`canUpdateRecordCondition` methods) — fixed in Task 4. Also expect an error because `policy-explainer.ts` doesn't exist yet (Task 5) — this file imports it. **This means Task 5 must actually be done before this typecheck can go clean; reorder your execution so Task 5 (`policy-explainer.ts`) is implemented before running this checkpoint for real, or accept this checkpoint shows both errors and treat it as informational.** Practically: implement Task 5 immediately after this step, then return to Task 4, then do one combined typecheck/test pass — see the note in Task 5.

---

### Task 4: Wire `CrudService` to `PermissionSnapshot`

**Files:**
- Modify: `src/core/crud/crud-service.ts` (full file)

**Interfaces:**
- Consumes: `PermissionService.loadSnapshot` (Task 3), `PermissionSnapshot`'s methods (Task 2).

- [ ] **Step 1: Replace the full contents of `crud-service.ts`**

```ts
import { and, eq, sql } from "drizzle-orm";
import type { z } from "zod";
import type { Database } from "../../infra/db/client";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { OutboxService } from "../outbox/outbox-service";
import type { PermissionService, RequestContext } from "../permission/permission-service";
import type { ListInput, QueryPlanner } from "../query/query-planner";
import type { WorkflowEngine } from "../workflow/workflow-engine";
import type { ServiceResult } from "./result";

type RecordDto = {
  id: string;
  entity: string;
  code: string | null;
  status: string | null;
  data: unknown;
  version: number;
  createdAt: Date;
  updatedAt: Date;
};

export class CrudService {
  constructor(
    private readonly db: Database,
    private readonly metadata: MetadataRegistry,
    private readonly queryPlanner: QueryPlanner,
    private readonly permissions: PermissionService,
    private readonly workflow: WorkflowEngine,
    private readonly outbox: OutboxService,
  ) {}

  async list(
    entityName: string,
    input: ListInput,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto[]>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canReadEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordPolicies = snapshot.getRecordPolicies("read");

    const plan = this.queryPlanner.planList(entity.name, input, context, recordPolicies);
    const rows = await this.db.client
      .select()
      .from(records)
      .where(plan.where)
      .orderBy(...plan.orderBy)
      .limit(plan.limit);

    const data = rows.map((row) => ({
      ...row,
      data: snapshot.filterReadableFields(context, row.data as Record<string, unknown>),
    }));

    return {
      ok: true,
      data,
      page: {
        limit: plan.limit,
      },
    };
  }

  async create(
    entityName: string,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canCreateEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), undefined);

    if (!writeDecision.allowed) {
      return { ok: false, status: 403, error: writeDecision.reason ?? "forbidden" };
    }

    const parsed = entity.schema.safeParse(rawData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }

    const data = parsed.data;
    const status = this.workflow.getInitialStatus(entity, data);

    const outcome = await this.db.client.transaction(async (tx) => {
      const inserted = await tx
        .insert(records)
        .values({
          tenantId: context.tenantId,
          entity: entity.name,
          code: typeof data.code === "string" ? data.code : null,
          status,
          data,
          createdBy: context.userId,
          updatedBy: context.userId,
        })
        .returning();

      const record = inserted[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.emitCreated(tx, entity, record.id, data);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 500, error: "insert_failed" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async update(
    entityName: string,
    id: string,
    expectedVersion: number,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canUpdateEntity(context, entity.name);

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
    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData);

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), existingData);

    if (!writeDecision.allowed) {
      return { ok: false, status: 403, error: writeDecision.reason ?? "forbidden" };
    }

    const mergedData: Record<string, unknown> = { ...existingData, ...rawData };

    if (entity.workflow) {
      // The state field can never change through this path — only `create` and
      // `transition` are allowed to move it — so it's always reset to its existing value.
      mergedData[entity.workflow.stateField] = existingData[entity.workflow.stateField];
    }

    const parsed = entity.schema.safeParse(mergedData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }

    const data = parsed.data;
    const code = typeof data.code === "string" ? data.code : null;

    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
        .update(records)
        .set({
          data,
          code,
          version: sql`${records.version} + 1`,
          updatedAt: new Date(),
          updatedBy: context.userId,
        })
        .where(
          and(
            eq(records.id, id),
            eq(records.tenantId, context.tenantId),
            eq(records.entity, entity.name),
            eq(records.version, expectedVersion),
            eq(records.deleted, false),
          ),
        )
        .returning();

      const record = updatedRows[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.emitUpdated(tx, entity, record.id, data, record.version);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async transition(
    entityName: string,
    id: string,
    action: string,
    expectedVersion: number,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canUpdateEntity(context, entity.name);

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
    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData);

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    if (!entity.workflow) {
      return { ok: false, status: 400, error: "no_workflow" };
    }

    const fromState = existingData[entity.workflow.stateField];

    if (typeof fromState !== "string") {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const transition = this.workflow.findTransition(entity, action, fromState);

    if (!transition) {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const guardResult = this.workflow.runGuard(transition, existingData, context);

    if (guardResult !== true) {
      return { ok: false, status: 422, error: "guard_failed", message: guardResult };
    }

    const nextData: Record<string, unknown> = {
      ...existingData,
      [entity.workflow.stateField]: transition.to,
    };

    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
        .update(records)
        .set({
          data: nextData,
          status: transition.to,
          version: sql`${records.version} + 1`,
          updatedAt: new Date(),
          updatedBy: context.userId,
        })
        .where(
          and(
            eq(records.id, id),
            eq(records.tenantId, context.tenantId),
            eq(records.entity, entity.name),
            eq(records.version, expectedVersion),
            eq(records.deleted, false),
          ),
        )
        .returning();

      const record = updatedRows[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.recordEvent(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context,
      );
      await this.workflow.emitTransitioned(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context.userId,
      );

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async flushOutbox() {
    await this.outbox.publishPending();
  }
}
```

Note the perf win: `transition()` previously called `getRecordPolicies` and (near the end) `getFieldPolicies` separately — 2 queries. It now calls `loadSnapshot` exactly once and reuses `snapshot` for both the record-condition check and the final response masking — 1 query. Same reduction applies to `list()` and `update()` (previously 2 separate `PermissionService` calls each, now 1 `loadSnapshot`).

- [ ] **Step 2: Run the tests to verify sub-project 3's enforcement tests still pass unmodified**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: PASS (all tests, including the 3 field/record enforcement tests from sub-project 3 — their behavior is unchanged, only the internal call pattern changed).

- [ ] **Step 3: Run the full permission-related suite together**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts src/core/permission/permission-snapshot.test.ts src/core/permission/policy-condition.test.ts src/core/crud/crud-service.test.ts src/core/query/query-planner.test.ts`
Expected: PASS across all five files. (This also confirms Task 3's `permission-service.ts` rewrite — including its `explain` method, which depends on `policy-explainer.ts` from Task 5 — actually compiles; if `policy-explainer.ts` doesn't exist yet, do Task 5 now before this step, per the note left in Task 3.)

- [ ] **Step 4: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors (assuming Task 5 is already done — see above).

---

### Task 5: `PolicyExplainer` — pure trace function

**Files:**
- Create: `src/core/permission/policy-explainer.ts`
- Create: `src/core/permission/policy-explainer.test.ts`

**Interfaces:**
- Consumes: `evaluateCondition`, `roleGatePassed` (`policy-condition.ts`), `PolicyRow`/`RequestContext` (`permission-service.ts`).
- Produces (consumed by Task 3's `PermissionService.explain`, already written): `PolicyTraceEntry`, `PolicyExplanation`, `explainPolicies(policyRows, context, subject): PolicyExplanation`.

**Note on sequencing:** `permission-service.ts` (Task 3) already imports from this file. If you're executing tasks in order, do this task's implementation (Steps 1-3 below) before running Task 3's or Task 4's verification steps — the checkpoints in those tasks call this out explicitly.

- [ ] **Step 1: Write the failing tests**

Create `src/core/permission/policy-explainer.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import type { PolicyRow, RequestContext } from "./permission-service";
import { explainPolicies } from "./policy-explainer";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
  roles: ["viewer"],
};

function policyRow(overrides: Partial<PolicyRow>): PolicyRow {
  return {
    id: "policy-1",
    tenantId: context.tenantId,
    entity: "crm.customers",
    action: "read",
    field: null,
    subject: "context",
    roles: null,
    condition: null,
    createdAt: new Date(),
    createdBy: null,
    ...overrides,
  };
}

describe("explainPolicies", () => {
  it("allows with an empty trace when there are no policies", () => {
    const result = explainPolicies([], context, undefined);
    expect(result).toEqual({ allowed: true, policiesConsidered: [] });
  });

  it("marks the role gate 'open' when the policy has no role restriction", () => {
    const result = explainPolicies([policyRow({ id: "p1" })], context, undefined);
    expect(result.allowed).toBe(true);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "open", conditionGate: "open" },
    ]);
  });

  it("marks the role gate 'failed' and short-circuits the condition gate", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          roles: ["editor"],
          condition: { attribute: "status", op: "eq", value: { literal: "active" } },
        }),
      ],
      context,
      { status: "active" },
    );
    expect(result.allowed).toBe(false);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "failed", conditionGate: "open" },
    ]);
  });

  it("marks the condition gate 'failed' with a reason when the role gate passes", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          roles: ["viewer"],
          condition: { attribute: "status", op: "eq", value: { literal: "active" } },
        }),
      ],
      context,
      { status: "draft" },
    );
    expect(result.allowed).toBe(false);
    expect(result.policiesConsidered).toHaveLength(1);
    expect(result.policiesConsidered[0]).toMatchObject({ policyId: "p1", roleGate: "passed", conditionGate: "failed" });
    expect(typeof result.policiesConsidered[0]?.conditionReason).toBe("string");
  });

  it("is allowed overall if any one policy fully passes, even if others fail", () => {
    const result = explainPolicies(
      [
        policyRow({ id: "p1", roles: ["editor"] }),
        policyRow({ id: "p2", roles: ["viewer"] }),
      ],
      context,
      undefined,
    );
    expect(result.allowed).toBe(true);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "failed", conditionGate: "open" },
      { policyId: "p2", roleGate: "open", conditionGate: "open" },
    ]);
  });

  it("uses the record subject only for policies with subject 'record'", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          subject: "record",
          condition: { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
        }),
      ],
      context,
      { createdBy: "00000000-0000-0000-0000-000000000002" },
    );
    expect(result.allowed).toBe(true);
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/policy-explainer.test.ts`
Expected: FAIL — the module `./policy-explainer` does not exist.

- [ ] **Step 3: Implement `policy-explainer.ts`**

Create `src/core/permission/policy-explainer.ts`:

```ts
import { evaluateCondition, roleGatePassed } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";
import type { PolicyRow, RequestContext } from "./permission-service";

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
  subject: Record<string, unknown> | undefined,
): PolicyExplanation {
  if (policyRows.length === 0) {
    return { allowed: true, policiesConsidered: [] };
  }

  const entries: PolicyTraceEntry[] = policyRows.map((row) => {
    const policyRoles = row.roles as string[] | null;
    const rolePassed = roleGatePassed(policyRoles, context.roles);
    const roleGate: PolicyTraceEntry["roleGate"] =
      !policyRoles || policyRoles.length === 0 ? "open" : rolePassed ? "passed" : "failed";

    if (!rolePassed) {
      return { policyId: row.id, roleGate, conditionGate: "open" };
    }

    const condition = row.condition as PolicyCondition | null;

    if (!condition) {
      return { policyId: row.id, roleGate, conditionGate: "open" };
    }

    const conditionSubject = row.subject === "record" && subject ? subject : context;
    const result = evaluateCondition(condition, conditionSubject, context);

    if (result === true) {
      return { policyId: row.id, roleGate, conditionGate: "passed" };
    }

    return { policyId: row.id, roleGate, conditionGate: "failed", conditionReason: result };
  });

  const allowed = entries.some(
    (entry) => entry.roleGate !== "failed" && entry.conditionGate !== "failed",
  );

  return { allowed, policiesConsidered: entries };
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/policy-explainer.test.ts`
Expected: PASS (6 tests).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors, and — if Task 3 was already done — `permission-service.ts` now compiles cleanly against this file.

---

### Task 6: `PermissionService.explain` tests

**Files:**
- Modify: `src/core/permission/permission-service.test.ts` (append tests)

**Interfaces:**
- Consumes: `PermissionService.explain` (already implemented in Task 3's rewrite).

- [ ] **Step 1: Write the tests**

Append to `src/core/permission/permission-service.test.ts`, inside the existing `describe("PermissionService (live DB)", ...)` block, before the closing `});`:

```ts

  it("explain reports an entity-level policy's role and condition gates", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        ["editor"],
        { attribute: "functionId", op: "eq", value: { literal: "sales-app" } },
        undefined,
      );

      const denied = await service.explain(contextWithRoles(["viewer"]), entity, "update");
      expect(denied.allowed).toBe(false);
      expect(denied.policiesConsidered).toHaveLength(1);
      expect(denied.policiesConsidered[0]?.roleGate).toBe("failed");

      const passing = await service.explain(
        contextWithRoles(["editor"], { functionId: "sales-app" }),
        entity,
        "update",
      );
      expect(passing.allowed).toBe(true);
      expect(passing.policiesConsidered[0]?.roleGate).toBe("passed");
      expect(passing.policiesConsidered[0]?.conditionGate).toBe("passed");
    } finally {
      await cleanup();
    }
  });

  it("explain reports a field-scoped policy when a field is given", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "read", ["hr"], undefined, undefined, "salary");

      const result = await service.explain(contextWithRoles(["viewer"]), entity, "read", {
        field: "salary",
      });
      expect(result.allowed).toBe(false);
      expect(result.policiesConsidered).toHaveLength(1);
    } finally {
      await cleanup();
    }
  });

  it("explain reports a record-scoped policy when a record is given", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        undefined,
        { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
        undefined,
        undefined,
        "record",
      );

      const owned = await service.explain(
        contextWithRoles(["editor"], { userId: "user-1" }),
        entity,
        "update",
        { record: { createdBy: "user-1" } },
      );
      expect(owned.allowed).toBe(true);

      const notOwned = await service.explain(
        contextWithRoles(["editor"], { userId: "user-1" }),
        entity,
        "update",
        { record: { createdBy: "someone-else" } },
      );
      expect(notOwned.allowed).toBe(false);
    } finally {
      await cleanup();
    }
  });
```

- [ ] **Step 2: Run the tests**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: PASS (all tests — the 8 remaining from before this plan, minus the 4 moved to `permission-snapshot.test.ts` in Task 3, plus these 3 new ones).

---

### Task 7: `POST /admin/policies/explain` — the policy simulator endpoint

**Files:**
- Modify: `src/server/routes/admin.ts`
- Modify: `src/server/routes/admin.test.ts` (append tests)

**Interfaces:**
- Consumes: `PermissionService.explain` (Task 3).

- [ ] **Step 1: Write the failing tests**

Append to `src/server/routes/admin.test.ts`, inside the existing `describe("admin routes (live DB)", ...)` block, before the closing `});`:

```ts

  it("simulates a decision for a hypothetical caller via /admin/policies/explain", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "update",
      ["editor"],
      undefined,
      undefined,
    );

    try {
      const deniedResponse = await app.inject({
        method: "POST",
        url: "/admin/policies/explain",
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { roles: ["viewer"], entity: "crm.customers", action: "update" },
      });

      expect(deniedResponse.statusCode).toBe(200);
      const denied = deniedResponse.json<{
        data: { allowed: boolean; policiesConsidered: { roleGate: string }[] };
      }>();
      expect(denied.data.allowed).toBe(false);
      expect(denied.data.policiesConsidered[0]?.roleGate).toBe("failed");

      const passingResponse = await app.inject({
        method: "POST",
        url: "/admin/policies/explain",
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { roles: ["editor"], entity: "crm.customers", action: "update" },
      });

      const passing = passingResponse.json<{ data: { allowed: boolean } }>();
      expect(passing.data.allowed).toBe(true);
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
    }
  });

  it("rejects a non-admin caller on /admin/policies/explain", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "POST",
      url: "/admin/policies/explain",
      headers: { authorization: `Bearer ${nonAdminToken}` },
      payload: { roles: ["admin"], entity: "crm.customers", action: "update" },
    });

    expect(response.statusCode).toBe(403);
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: FAIL — the route doesn't exist (404s).

- [ ] **Step 3: Add the route**

In `src/server/routes/admin.ts`, add this import:

```ts
import type { RequestContext } from "../../core/permission/permission-service";
```

Add this schema after `ListPoliciesQuerySchema`:

```ts
const ExplainBodySchema = z.object({
  roles: z.array(z.string()),
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update", "write"]),
  field: z.string().optional(),
  record: z.record(z.unknown()).optional(),
});
```

Add this route at the end of `registerAdminRoutes`, after the `DELETE /admin/policies/:id` route and before the function's closing `}`:

```ts

  app.post<{ Body: z.infer<typeof ExplainBodySchema> }>(
    "/admin/policies/explain",
    { schema: { body: zodToJsonSchema(ExplainBodySchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const body = ExplainBodySchema.parse(request.body);
      const simulatedContext: RequestContext = {
        tenantId: request.context.tenantId,
        roles: body.roles,
      };
      const explanation = await container.permissions.explain(
        simulatedContext,
        body.entity,
        body.action,
        { field: body.field, record: body.record as Record<string, unknown> | undefined },
      );
      return { data: explanation };
    },
  );
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: PASS (all tests: the 8 from sub-projects 1-3 plus these 2 new ones).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 8: Full verification + manual E2E

**Files:** none (verification only).

- [ ] **Step 1: Full test suite**

Run: `pnpm test`
Expected: every test file passes.

- [ ] **Step 2: Typecheck and lint**

Run: `pnpm typecheck`
Expected: no new errors.

Run: `pnpm eslint src/core/permission/policy-condition.ts src/core/permission/policy-condition.test.ts src/core/permission/permission-snapshot.ts src/core/permission/permission-snapshot.test.ts src/core/permission/permission-service.ts src/core/permission/permission-service.test.ts src/core/permission/policy-explainer.ts src/core/permission/policy-explainer.test.ts src/core/crud/crud-service.ts src/server/routes/admin.ts src/server/routes/admin.test.ts`
Expected: clean, aside from the same pre-existing jsonb-cast/unused-import warnings already accepted throughout this initiative (in `crud-service.ts`).

- [ ] **Step 3: Manual verification — the policy simulator**

Start `pnpm dev` if not already running, then:

```bash
pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
ADMIN_TOKEN=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002)

# restrict updating crm.customers to the "editor" role
curl -s -X POST http://localhost:3000/admin/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","action":"update","roles":["editor"]}'

# simulate: would a bare "viewer" be allowed to update? expect allowed:false, roleGate:"failed"
curl -s -X POST http://localhost:3000/admin/policies/explain \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"roles":["viewer"],"entity":"crm.customers","action":"update"}'

# simulate: would "editor" be allowed? expect allowed:true
curl -s -X POST http://localhost:3000/admin/policies/explain \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"roles":["editor"],"entity":"crm.customers","action":"update"}'
```

Confirm both responses match. Clean up:

```bash
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM policies WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM user_roles WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
```

Stop the dev server afterward if you started a new one for this.

---

## Plan Self-Review Notes

- **Spec coverage:** §1 (`PermissionSnapshot`) → Tasks 1-2. §2 (`PolicyExplainer`) → Task 5. §3 (debug endpoint) → Task 7. §4 (consolidated tests) → Tasks 2, 5, 6, 7 (no new files beyond what's needed; `permission-snapshot.test.ts` and `policy-explainer.test.ts` are new because they test genuinely new modules, matching this repo's one-test-file-per-module convention — the spec's "no new test files" was written before `PermissionSnapshot`/`PolicyExplainer` were split into their own files, superseded by that later decision).
- **No placeholders:** every step has literal code.
- **Type consistency checked:** `PermissionSnapshot`'s constructor/method signatures (Task 2) match `PermissionService.loadSnapshot`'s return type (Task 3) and every call site in `CrudService` (Task 4). `explainPolicies`'s signature (Task 5) matches `PermissionService.explain`'s call to it (Task 3) exactly (`(rows, context, subject)` order). `PolicyExplanation`/`PolicyTraceEntry` (Task 5) match the JSON shape asserted in Task 7's route tests.
- **Sequencing risk flagged explicitly:** Task 3 depends on Task 5 (`policy-explainer.ts`) to typecheck cleanly, but Task 5 is listed after Task 3 in reading order (it was easier to explain `PermissionService`'s full new shape in one place). Both Task 3 and Task 5 call this out inline so whoever executes the plan doesn't get stuck on a checkpoint that can only go green once both tasks are done.
- **Deferred, not silently dropped:** the two sub-project-3 bugs (admin bypass gap in SQL list filtering; incomplete top-level field masking) are named explicitly in Global Constraints so they aren't lost, but no task here fixes them — matches the user's explicit request to keep them for a separate plan.
