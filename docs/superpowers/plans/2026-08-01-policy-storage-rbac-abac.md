# Policy Storage + RBAC/ABAC Evaluator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move policy definition out of static `*.entity.ts` declarations and into a database-backed, per-tenant, dynamically editable `policies` table — `PermissionService` becomes an async RBAC+ABAC evaluator, with a minimal admin API to manage policies.

**Architecture:** New `policies` table + a generic, reusable `evaluateCondition` function (`src/core/permission/policy-condition.ts`) that both this round and sub-project 3 can call unchanged. `PermissionService` drops its `MetadataRegistry` dependency (no longer needed) in favor of `Database`, and `checkAction` becomes async: admin bypass, then query policies for `(tenant, entity, action)`, OR across whatever's found. Admin routes for policy CRUD mirror sub-project 1's `/admin/users/*` pattern.

**Tech Stack:** Fastify, Zod, Drizzle ORM, PostgreSQL, vitest (live-DB integration tests).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-01-policy-storage-rbac-abac-design.md` — every task implements a section of it.
- This is sub-project 2 of 4. Field/record-level enforcement (sub-project 3) and `PolicyExplainer`/`PermissionSnapshotCache` (sub-project 4) are **out of scope**.
- No deny rules — absence of a matching policy still means allowed (matches today's default-open behavior).
- No policy update (PATCH) — delete and recreate.
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for the user to review at the end.
- `docker compose up -d postgres rabbitmq` must be running for any test/migration step.
- Run `pnpm typecheck` after any type-level change. Pre-existing errors in `src/infra/messaging/rabbitmq.ts` are known and unrelated — ignore them.

### A known, expected breakage window

`PermissionService.checkAction`/`canReadEntity`/`canCreateEntity`/`canUpdateEntity` become `async` in Task 3. `src/core/crud/crud-service.ts` has four call sites that currently call them without `await` (`decision.allowed` on an un-awaited call becomes `Promise<PermissionDecision>.allowed`, which is `undefined` — every request would 403). **This is a compile-time TypeScript error, not a silent bug** — `pnpm typecheck` after Task 3 is expected to show exactly four errors pointing at these call sites (`src/core/crud/crud-service.ts` lines 44, 78, 138, 232 as of this plan). Task 5 fixes them. Do not attempt to "fix" this in Task 3 — the tasks are ordered so Task 4's tests exercise `PermissionService` directly (no `CrudService` in the way), and Task 5 is a small, isolated mechanical fix.

---

### Task 1: `policies` table + migration

**Files:**
- Modify: `src/infra/db/schema.ts` (add table after `userRoles`, before `recordRelations`)

**Interfaces:**
- Produces: `policies` Drizzle table — columns `id, tenantId, entity, action, roles, condition, createdAt, createdBy` — consumed by Task 3's `PermissionService`.

- [ ] **Step 1: Add the `policies` table**

In `src/infra/db/schema.ts`, insert this block after the `userRoles` table definition, before `export const recordRelations = relations(records, () => ({}));`. No new imports are needed — `jsonb`, `index`, `timestamp`, `uuid`, `varchar`, `pgTable` are already imported.

```ts
export const policies = pgTable(
  "policies",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    action: varchar("action", { length: 20 }).notNull(),
    roles: jsonb("roles"),
    condition: jsonb("condition"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    createdBy: uuid("created_by"),
  },
  (table) => ({
    tenantEntityActionIdx: index("policies_tenant_entity_action_idx").on(
      table.tenantId,
      table.entity,
      table.action,
    ),
  }),
);
```

`roles`/`condition` are left as plain untyped `jsonb` (no `.$type<T>()`), matching the existing convention in this file (`records.data`, `outboxEvents.payload` are also untyped jsonb, cast at the read site instead) — this also avoids `src/infra/db/schema.ts` importing types from `src/core/permission/`, keeping infra decoupled from core.

- [ ] **Step 2: Generate the migration**

Run: `pnpm db:generate`
Expected: a new file under `src/infra/db/migrations/` with `CREATE TABLE "policies"` and an index on `(tenant_id, entity, action)`. Open it, confirm no unrelated diffs.

- [ ] **Step 3: Apply the migration**

Run: `pnpm db:migrate`
Expected: exits 0.

- [ ] **Step 4: Verify the table exists**

Run: `docker compose exec postgres psql -U metap -d metap -c '\d policies'`
Expected: 8 columns, one index (plus the primary key).

---

### Task 2: `PolicyCondition` type + generic evaluator

**Files:**
- Create: `src/core/permission/policy-condition.ts`
- Test: `src/core/permission/policy-condition.test.ts` (new)

**Interfaces:**
- Consumes: `RequestContext` (`src/core/permission/permission-service.ts`).
- Produces (consumed by Task 3's `PermissionService`, and later sub-project 3):
  - `type PolicyValue = { literal: unknown } | { fromContext: keyof RequestContext }`
  - `type PolicyCondition = { attribute: string; op: "eq"|"neq"|"in"|"notIn"; value: PolicyValue } | { all: readonly PolicyCondition[] } | { any: readonly PolicyCondition[] }`
  - `evaluateCondition(condition: PolicyCondition, subject: Record<string, unknown>, context: RequestContext): true | string`

- [ ] **Step 1: Write the failing tests**

Create `src/core/permission/policy-condition.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import type { RequestContext } from "./permission-service";
import { evaluateCondition } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
  functionId: "sales-app",
};

describe("evaluateCondition", () => {
  it("passes an eq condition against a literal value", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "eq",
      value: { literal: "active" },
    };
    expect(evaluateCondition(condition, { status: "active" }, context)).toBe(true);
  });

  it("fails an eq condition and returns a string reason", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "eq",
      value: { literal: "active" },
    };
    const result = evaluateCondition(condition, { status: "draft" }, context);
    expect(result).not.toBe(true);
    expect(typeof result).toBe("string");
  });

  it("evaluates neq", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "neq",
      value: { literal: "blocked" },
    };
    expect(evaluateCondition(condition, { status: "active" }, context)).toBe(true);
    expect(evaluateCondition(condition, { status: "blocked" }, context)).not.toBe(true);
  });

  it("evaluates in and notIn against a literal array", () => {
    const inCondition: PolicyCondition = {
      attribute: "status",
      op: "in",
      value: { literal: ["draft", "active"] },
    };
    expect(evaluateCondition(inCondition, { status: "active" }, context)).toBe(true);
    expect(evaluateCondition(inCondition, { status: "blocked" }, context)).not.toBe(true);

    const notInCondition: PolicyCondition = {
      attribute: "status",
      op: "notIn",
      value: { literal: ["blocked"] },
    };
    expect(evaluateCondition(notInCondition, { status: "active" }, context)).toBe(true);
  });

  it("resolves value from context via fromContext", () => {
    const condition: PolicyCondition = {
      attribute: "createdBy",
      op: "eq",
      value: { fromContext: "userId" },
    };
    expect(
      evaluateCondition(condition, { createdBy: "00000000-0000-0000-0000-000000000002" }, context),
    ).toBe(true);
    expect(
      evaluateCondition(condition, { createdBy: "someone-else" }, context),
    ).not.toBe(true);
  });

  it("requires every condition in 'all' to pass", () => {
    const condition: PolicyCondition = {
      all: [
        { attribute: "status", op: "eq", value: { literal: "active" } },
        { attribute: "region", op: "eq", value: { literal: "vn" } },
      ],
    };
    expect(evaluateCondition(condition, { status: "active", region: "vn" }, context)).toBe(true);
    expect(
      evaluateCondition(condition, { status: "active", region: "us" }, context),
    ).not.toBe(true);
  });

  it("requires at least one condition in 'any' to pass", () => {
    const condition: PolicyCondition = {
      any: [
        { attribute: "status", op: "eq", value: { literal: "active" } },
        { attribute: "status", op: "eq", value: { literal: "draft" } },
      ],
    };
    expect(evaluateCondition(condition, { status: "draft" }, context)).toBe(true);
    expect(evaluateCondition(condition, { status: "blocked" }, context)).not.toBe(true);
  });

  it("evaluates a context-only condition using context as its own subject", () => {
    const condition: PolicyCondition = {
      attribute: "functionId",
      op: "eq",
      value: { literal: "sales-app" },
    };
    expect(
      evaluateCondition(condition, context as unknown as Record<string, unknown>, context),
    ).toBe(true);
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/policy-condition.test.ts`
Expected: FAIL — the module `./policy-condition` does not exist.

- [ ] **Step 3: Implement `policy-condition.ts`**

Create `src/core/permission/policy-condition.ts`:

```ts
import type { RequestContext } from "./permission-service";

export type PolicyValue = { literal: unknown } | { fromContext: keyof RequestContext };

export type PolicyCondition =
  | { attribute: string; op: "eq" | "neq" | "in" | "notIn"; value: PolicyValue }
  | { all: readonly PolicyCondition[] }
  | { any: readonly PolicyCondition[] };

function resolveValue(value: PolicyValue, context: RequestContext): unknown {
  return "literal" in value ? value.literal : context[value.fromContext];
}

function matchOperator(
  op: "eq" | "neq" | "in" | "notIn",
  actual: unknown,
  expected: unknown,
): boolean {
  switch (op) {
    case "eq":
      return actual === expected;
    case "neq":
      return actual !== expected;
    case "in":
      return Array.isArray(expected) && expected.includes(actual);
    case "notIn":
      return Array.isArray(expected) && !expected.includes(actual);
  }
}

export function evaluateCondition(
  condition: PolicyCondition,
  subject: Record<string, unknown>,
  context: RequestContext,
): true | string {
  if ("all" in condition) {
    for (const inner of condition.all) {
      const result = evaluateCondition(inner, subject, context);
      if (result !== true) {
        return result;
      }
    }
    return true;
  }

  if ("any" in condition) {
    let lastFailure: string | undefined;
    for (const inner of condition.any) {
      const result = evaluateCondition(inner, subject, context);
      if (result === true) {
        return true;
      }
      lastFailure = result;
    }
    return lastFailure ?? "no condition in 'any' matched";
  }

  const actual = subject[condition.attribute];
  const expected = resolveValue(condition.value, context);
  const passed = matchOperator(condition.op, actual, expected);

  return passed
    ? true
    : `condition failed: ${condition.attribute} ${condition.op} ${JSON.stringify(expected)} (got ${JSON.stringify(actual)})`;
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/policy-condition.test.ts`
Expected: PASS (8 tests).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 3: `PermissionService` — async, DB-backed evaluator + policy CRUD

**Files:**
- Modify: `src/core/permission/permission-service.ts` (full file)
- Modify: `src/core/metadata/entity.ts` (remove `EntityPermissions`)

**Interfaces:**
- Consumes: `policies` table (Task 1), `evaluateCondition` (Task 2), `Database` (`src/infra/db/client.ts`).
- Produces (consumed by Task 5's `CrudService`/`container.ts` and Task 6's admin routes):
  - `canReadEntity(context, entity): Promise<PermissionDecision>`
  - `canCreateEntity(context, entity): Promise<PermissionDecision>`
  - `canUpdateEntity(context, entity): Promise<PermissionDecision>`
  - `scopedTenant(context): string` (unchanged, still sync)
  - `listPolicies(tenantId: string, entity?: string): Promise<PolicyRow[]>`
  - `createPolicy(tenantId: string, entity: string, action: EntityAction, roles: string[] | undefined, condition: PolicyCondition | undefined, createdBy: string | undefined): Promise<PolicyRow>`
  - `deletePolicy(tenantId: string, id: string): Promise<void>`
  - `export type EntityAction = "read" | "create" | "update"` (now exported, was private to the file before)

- [ ] **Step 1: Replace the full contents of `permission-service.ts`**

```ts
import { and, eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluateCondition } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";

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
        ),
      );

    if (rows.length === 0) {
      return { allowed: true };
    }

    const callerRoles = context.roles ?? [];

    for (const policy of rows) {
      const allowedRoles = policy.roles as string[] | null;
      const rolePassed =
        !allowedRoles ||
        allowedRoles.length === 0 ||
        callerRoles.some((role) => allowedRoles.includes(role));

      if (!rolePassed) {
        continue;
      }

      const condition = policy.condition as PolicyCondition | null;

      if (!condition) {
        return { allowed: true };
      }

      const conditionResult = evaluateCondition(
        condition,
        context as unknown as Record<string, unknown>,
        context,
      );

      if (conditionResult === true) {
        return { allowed: true };
      }
    }

    return { allowed: false, reason: "forbidden" };
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

  async listPolicies(tenantId: string, entity?: string) {
    const where = entity
      ? and(eq(policies.tenantId, tenantId), eq(policies.entity, entity))
      : eq(policies.tenantId, tenantId);

    return this.db.client.select().from(policies).where(where);
  }

  async createPolicy(
    tenantId: string,
    entity: string,
    action: EntityAction,
    roles: string[] | undefined,
    condition: PolicyCondition | undefined,
    createdBy: string | undefined,
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
      })
      .returning();

    return inserted[0];
  }

  async deletePolicy(tenantId: string, id: string): Promise<void> {
    await this.db.client
      .delete(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.id, id)));
  }
}
```

Note: the `metadata: MetadataRegistry` constructor parameter is gone. It was only ever used to read `entity.permissions`, which no longer exists after Step 2 below — every caller (`CrudService`) already validates the entity exists before calling a permission check, so `PermissionService` doesn't need to re-resolve it.

- [ ] **Step 2: Delete the static `EntityPermissions` mechanism**

In `src/core/metadata/entity.ts`, remove the `EntityPermissions` type entirely:

```ts
export type EntityPermissions = {
  read?: readonly string[];
  create?: readonly string[];
  update?: readonly string[];
};
```

and remove the `permissions?: EntityPermissions;` field from `EntityDefinition`. No entity currently sets it (verified by grep during spec-writing), so this is a clean removal.

- [ ] **Step 3: Typecheck — confirm the expected breakage**

Run: `pnpm typecheck`
Expected: errors at `src/core/container.ts` (still calling `new PermissionService(metadata)` with the old signature), `src/core/crud/crud-service.ts` (four `.allowed`/access-on-`Promise` errors, per the Global Constraints note), and `src/core/permission/permission-service.test.ts` (still using the old sync API and the now-deleted `entity.permissions`). All three are fixed in later tasks — this step is a checkpoint, not a stopping point.

---

### Task 4: Rewrite `permission-service.test.ts` as a live-DB suite

**Files:**
- Modify: `src/core/permission/permission-service.test.ts` (full file)

**Interfaces:**
- Consumes: `PermissionService` (Task 3), `createDatabase` (`src/infra/db/client.ts`).

- [ ] **Step 1: Replace the full contents of `permission-service.test.ts`**

```ts
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";

const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";

describe("PermissionService (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let service: PermissionService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000060";
  const entity = "test.restricted";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new PermissionService(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping PermissionService live-DB tests: could not connect to ${databaseUrl}: ${
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

  it("allows admin regardless of any policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "create", ["editor"], undefined, undefined);
      const decision = await service.canCreateEntity(contextWithRoles(["admin"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("allows any role when the entity has no policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const decision = await service.canReadEntity(
      contextWithRoles(["nobody-in-particular"]),
      entity,
    );
    expect(decision.allowed).toBe(true);
  });

  it("allows a role listed on a matching policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "read", ["viewer", "editor"], undefined, undefined);
      const decision = await service.canReadEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("denies a role not listed on any matching policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "create", ["editor"], undefined, undefined);
      const decision = await service.canCreateEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(false);
      expect(decision.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("evaluates a condition gate in addition to the role gate", async (ctx) => {
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

      const passing = await service.canUpdateEntity(
        contextWithRoles(["editor"], { functionId: "sales-app" }),
        entity,
      );
      expect(passing.allowed).toBe(true);

      const failing = await service.canUpdateEntity(
        contextWithRoles(["editor"], { functionId: "other-app" }),
        entity,
      );
      expect(failing.allowed).toBe(false);
      expect(failing.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("ORs multiple policies for the same action: one passing is enough", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "update", ["impossible-role"], undefined, undefined);
      await service.createPolicy(tenantId, entity, "update", ["editor"], undefined, undefined);

      const decision = await service.canUpdateEntity(contextWithRoles(["editor"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("does not apply another tenant's policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const otherTenantId = "00000000-0000-0000-0000-000000000061";

    try {
      await service.createPolicy(otherTenantId, entity, "create", ["editor"], undefined, undefined);

      const decision = await service.canCreateEntity(
        contextWithRoles(["nobody-in-particular"]),
        entity,
      );
      expect(decision.allowed).toBe(true);
    } finally {
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [otherTenantId]);
    }
  });
});
```

- [ ] **Step 2: Run the tests**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: PASS (7 tests). If this fails, check that Task 3's `checkAction` logic matches: admin bypass first, then no-rows-allow, then per-policy role+condition AND, OR across policies.

---

### Task 5: Fix `CrudService`'s missing `await`s + `container.ts` wiring

**Files:**
- Modify: `src/core/crud/crud-service.ts` (four call sites)
- Modify: `src/core/container.ts` (full file)

**Interfaces:**
- Consumes: `PermissionService`'s new constructor (`db: Database` instead of `metadata: MetadataRegistry`) and async `canXEntity` methods (Task 3).

- [ ] **Step 1: Add `await` to the four permission checks in `crud-service.ts`**

In `src/core/crud/crud-service.ts`, change each of these four lines (currently at lines 44, 78, 138, 232 — confirm with `grep -n "this.permissions.can" src/core/crud/crud-service.ts` since line numbers may have shifted):

```ts
    const decision = this.permissions.canReadEntity(context, entity.name);
```

```ts
    const decision = this.permissions.canCreateEntity(context, entity.name);
```

```ts
    const decision = this.permissions.canUpdateEntity(context, entity.name);
```

(the last one appears twice — once in `update`, once in `transition`)

to their `await`-prefixed form, e.g.:

```ts
    const decision = await this.permissions.canReadEntity(context, entity.name);
```

— same pattern for all four (`canReadEntity` ×1, `canCreateEntity` ×1, `canUpdateEntity` ×2). Every one of these four call sites is already inside an `async` method (`list`, `create`, `update`, `transition`), so no other signature changes are needed in this file.

- [ ] **Step 2: Update `container.ts`'s `PermissionService` construction**

Replace the full contents of `src/core/container.ts` with:

```ts
import type { AppConfig } from "../server/config";
import { createJwtVerifier } from "./auth/jwt-verifier";
import { RoleAssignmentService } from "./auth/role-assignment-service";
import { createDatabase } from "../infra/db/client";
import { createRabbitPublisher } from "../infra/messaging/rabbitmq";
import { customerEntity } from "../modules/crm/customer.entity";
import { CrudService } from "./crud/crud-service";
import { HealthService } from "./health/health-service";
import { MetadataRegistry } from "./metadata/metadata-registry";
import { OutboxService } from "./outbox/outbox-service";
import { PermissionService } from "./permission/permission-service";
import { QueryPlanner } from "./query/query-planner";
import { WorkflowEngine } from "./workflow/workflow-engine";

export function createContainer(config: AppConfig) {
  const db = createDatabase(config.databaseUrl);
  const auth = createJwtVerifier(config.authJwtPublicKeyPath);
  const roleAssignments = new RoleAssignmentService(db);
  const rabbit = createRabbitPublisher(config.rabbitmqUrl);

  const metadata = new MetadataRegistry();
  metadata.register(customerEntity);

  const permissions = new PermissionService(db);
  const queryPlanner = new QueryPlanner(metadata, permissions);
  const outbox = new OutboxService(db, rabbit);
  const workflow = new WorkflowEngine(outbox);
  const crud = new CrudService(db, metadata, queryPlanner, permissions, workflow, outbox);
  const health = new HealthService(db);

  return {
    db,
    auth,
    roleAssignments,
    rabbit,
    metadata,
    permissions,
    queryPlanner,
    outbox,
    workflow,
    crud,
    health,
    async close() {
      await rabbit.close();
      await db.close();
    },
  };
}

export type AppContainer = ReturnType<typeof createContainer>;
```

(Only change from the current file: `new PermissionService(metadata)` → `new PermissionService(db)`.)

- [ ] **Step 3: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors (the four `crud-service.ts` errors and the `container.ts` error from Task 3's checkpoint are now resolved).

- [ ] **Step 4: Run the full test suite**

Run: `pnpm test`
Expected: all tests pass, including `src/core/crud/crud-service.test.ts` and `src/server/app.test.ts` unmodified — `crm.customers` has no policies, so `checkAction`'s "no rows → allow" path keeps their behavior identical to before this sub-project.

---

### Task 6: Admin API — `/admin/policies`

**Files:**
- Modify: `src/server/routes/admin.ts` (add imports, schemas, three routes)
- Modify: `src/server/routes/admin.test.ts` (append new tests)

**Interfaces:**
- Consumes: `container.permissions.listPolicies/createPolicy/deletePolicy` (Task 3).

- [ ] **Step 1: Write the failing tests**

Append to `src/server/routes/admin.test.ts`, inside the existing `describe("admin routes (live DB)", ...)` block (after the last `it(...)`, before the closing `});`), these new cases. Also extend the `afterAll` cleanup to delete from `policies` too — change:

```ts
  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });
```

to:

```ts
  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1", [tenantId]);
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });
```

Then add, before the final closing `});` of the describe block:

```ts

  it("rejects a non-admin caller with 403 on /admin/policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${nonAdminToken}` },
    });

    expect(response.statusCode).toBe(403);
  });

  it("creates a policy, lists it filtered by entity, then deletes it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { entity: "crm.customers", action: "update", roles: ["editor"] },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{ data: { id: string; entity: string } }>();
    expect(created.data.entity).toBe("crm.customers");

    const listResponse = await app.inject({
      method: "GET",
      url: "/admin/policies?entity=crm.customers",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(listResponse.statusCode).toBe(200);
    const listed = listResponse.json<{ data: { id: string }[] }>();
    expect(listed.data.some((p) => p.id === created.data.id)).toBe(true);

    const deleteResponse = await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(deleteResponse.statusCode).toBe(200);

    const afterDelete = await app.inject({
      method: "GET",
      url: "/admin/policies?entity=crm.customers",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    const afterDeleteBody = afterDelete.json<{ data: { id: string }[] }>();
    expect(afterDeleteBody.data.some((p) => p.id === created.data.id)).toBe(false);
  });

  it("accepts a policy with a condition", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: {
        entity: "crm.customers",
        action: "read",
        condition: { attribute: "functionId", op: "eq", value: { literal: "sales-app" } },
      },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{ data: { id: string } }>();

    await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: FAIL — `/admin/policies` routes don't exist (404s).

- [ ] **Step 3: Add the policy routes to `admin.ts`**

In `src/server/routes/admin.ts`, add these imports after the existing ones:

```ts
import type { EntityAction } from "../../core/permission/permission-service";
import type { PolicyCondition } from "../../core/permission/policy-condition";
```

Add these schemas after `AssignRoleBodySchema`:

```ts
const PolicyValueSchema = z.union([
  z.object({ literal: z.unknown() }),
  z.object({ fromContext: z.enum(["tenantId", "userId", "roles", "functionId"]) }),
]);

const PolicyConditionSchema: z.ZodType<PolicyCondition> = z.lazy(() =>
  z.union([
    z.object({
      attribute: z.string(),
      op: z.enum(["eq", "neq", "in", "notIn"]),
      value: PolicyValueSchema,
    }),
    z.object({ all: z.array(PolicyConditionSchema) }),
    z.object({ any: z.array(PolicyConditionSchema) }),
  ]),
);

const CreatePolicyBodySchema = z.object({
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update"]),
  roles: z.array(z.string()).optional(),
  condition: PolicyConditionSchema.optional(),
});

const PolicyIdParamsSchema = z.object({ id: z.string().uuid() });
const ListPoliciesQuerySchema = z.object({ entity: z.string().optional() });
```

Add these three routes at the end of `registerAdminRoutes`, before its closing `}`:

```ts

  app.get<{ Querystring: { entity?: string } }>(
    "/admin/policies",
    { schema: { querystring: zodToJsonSchema(ListPoliciesQuerySchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const query = ListPoliciesQuerySchema.parse(request.query);
      const rows = await container.permissions.listPolicies(request.context.tenantId, query.entity);
      return { data: rows };
    },
  );

  app.post<{ Body: z.infer<typeof CreatePolicyBodySchema> }>(
    "/admin/policies",
    { schema: { body: zodToJsonSchema(CreatePolicyBodySchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const body = CreatePolicyBodySchema.parse(request.body);
      const created = await container.permissions.createPolicy(
        request.context.tenantId,
        body.entity,
        body.action as EntityAction,
        body.roles,
        body.condition,
        request.context.userId,
      );
      return reply.code(201).send({ data: created });
    },
  );

  app.delete<{ Params: { id: string } }>(
    "/admin/policies/:id",
    { schema: { params: zodToJsonSchema(PolicyIdParamsSchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = PolicyIdParamsSchema.parse(request.params);
      await container.permissions.deletePolicy(request.context.tenantId, params.id);
      return { data: null };
    },
  );
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: PASS (7 tests: 4 from sub-project 1 + 3 new).

- [ ] **Step 5: Full test suite, typecheck, lint**

Run: `pnpm test && pnpm typecheck`
Expected: all tests pass; no new typecheck errors.

Run: `pnpm eslint src/server/routes/admin.ts src/server/routes/admin.test.ts src/core/permission/permission-service.ts src/core/permission/policy-condition.ts src/core/permission/policy-condition.test.ts src/core/permission/permission-service.test.ts src/core/crud/crud-service.ts src/core/container.ts src/infra/db/schema.ts`
Expected: any `@typescript-eslint/no-unsafe-*` warnings on the `policy.roles as string[] | null` / `policy.condition as PolicyCondition | null` casts in `permission-service.ts` are the same class of warning already accepted as pre-existing on `existing.data as Record<string, unknown>` in `crud-service.ts` (both are casts off an untyped jsonb column) — not a regression to fix here. Everything else should be clean.

- [ ] **Step 6: Manual verification against the dev server**

Start `pnpm dev` if not already running, then:

```bash
pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
TOKEN=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002)

# create a customer, restrict "update" on crm.customers to "editor" only
curl -s -X POST http://localhost:3000/admin/policies \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","action":"update","roles":["editor"]}'

# create a customer as admin (create isn't restricted, still works)
CUSTOMER=$(curl -s -X POST http://localhost:3000/api/crm.customers \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"POL1","name":"Policy Test"}}')
echo "$CUSTOMER"

# admin can still update (admin bypasses all policies)
# extract the id/version from $CUSTOMER manually and try:
# curl -X PATCH http://localhost:3000/api/crm.customers/<id> -H "Authorization: Bearer $TOKEN" ... -d '{"version":1,"data":{"name":"Policy Test 2"}}'
# expect 200

# a non-admin, non-editor user should now be blocked from updating
TOKEN2=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000098)
# curl -X PATCH http://localhost:3000/api/crm.customers/<id> -H "Authorization: Bearer $TOKEN2" ... -d '{"version":1,"data":{"name":"Blocked"}}'
# expect 403
```

Confirm the 403 actually happens for the non-editor, non-admin caller — this is the concrete proof the whole sub-project works end to end. Clean up afterward:

```bash
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM policies WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM user_roles WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM records WHERE code = 'POL1';"
```

Stop the dev server afterward if you started a new one for this.

---

## Plan Self-Review Notes

- **Spec coverage:** §1 (`policies` schema) → Task 1. §2 (`PolicyCondition`/evaluator) → Task 2. §3 (`PermissionService` rewrite) → Task 3. §4 (admin API) → Task 6. §5 (delete static `EntityPermissions`) → Task 3, Step 2 (a gap found during self-review — initially unassigned, fixed inline by folding it into Task 3 rather than leaving it as a bolted-on addendum). "Consequences for existing code" → Tasks 4 and 5.
- **No placeholders:** every step has literal code.
- **Type consistency checked:** `PermissionService.canReadEntity`/`canCreateEntity`/`canUpdateEntity` return `Promise<PermissionDecision>` consistently across Task 3 (definition), Task 4 (test usage with `await`), and Task 5 (`crud-service.ts` call sites, now `await`ed). `listPolicies`/`createPolicy`/`deletePolicy` signatures in Task 3 match their call sites in Task 6's `admin.ts` and Task 4's tests exactly. `PolicyCondition`/`evaluateCondition` signatures in Task 2 match their only consumer, `PermissionService.checkAction` in Task 3.
