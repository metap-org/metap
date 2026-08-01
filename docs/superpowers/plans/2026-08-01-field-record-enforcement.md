# Field-Level + Record-Level Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire field-level masking (hide on read, block on write) and record-level conditional access (evaluated against actual record data, pushed to SQL for `list()`) into `CrudService` and `QueryPlanner`, on top of sub-project 2's entity-level `policies` table and `evaluateCondition` evaluator.

**Architecture:** `policies` gains `field` and `subject` columns. A new `condition-to-sql.ts` module translates `PolicyCondition` trees into Drizzle SQL (mirroring `evaluateCondition`'s recursion) for `QueryPlanner`. `PermissionService` gains field/record policy fetchers and pure evaluation helpers (`filterReadableFields`, `assertWritableFields`, `canUpdateRecordCondition`) built on a shared `evaluatePolicyRow` + `roleGatePassed` primitive. `CrudService`'s four methods each fetch the relevant policies once and apply them.

**Tech Stack:** Fastify, Zod, Drizzle ORM, PostgreSQL, vitest (live-DB integration tests).

## Global Constraints

- Specs: `docs/superpowers/specs/2026-08-01-field-record-enforcement-design.md` (this plan) and `docs/superpowers/specs/2026-08-01-policy-explainer-snapshot-cache-design.md` (next plan, not this one).
- `PolicyExplainer`, the `/admin/policies/explain` endpoint, and formalizing `PermissionSnapshotCache` as its own type are **out of scope** — sub-project 4.
- No cross-request caching — every `CrudService` method fetches policies fresh, once per call.
- A refinement over the spec, decided during planning: field-write checks use `Object.keys(rawData)` (the caller's actual payload keys, before Zod parsing/defaulting) rather than post-parse keys — this avoids flagging a Zod-applied default (e.g. `status: "draft"`) as an "attempted write" the caller never made.
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for review.
- `docker compose up -d postgres rabbitmq` must be running for any test/migration step.
- Run `pnpm typecheck` after any type-level change. Pre-existing errors in `src/infra/messaging/rabbitmq.ts` are known and unrelated.

### A correctness fix bundled into this plan

`PermissionService.checkAction`'s query currently (sub-project 2) does
`eq(policies.entity, entityName)` + `eq(policies.action, action)` with no
`field` filter. Once field-scoped rows exist in the same table with
overlapping `action` values (field-level `"read"` vs. entity-level
`"read"`), `checkAction` would incorrectly also match field-scoped rows
when checking entity-level `read`. Task 5 adds `isNull(policies.field)` to
`checkAction`'s query — this is a real bug fix necessitated by the shared
schema, not an unrelated change; it's called out explicitly because it's
easy to miss.

---

### Task 1: Extend `policies` with `field` and `subject` columns

**Files:**
- Modify: `src/infra/db/schema.ts`

**Interfaces:**
- Produces: `policies.field: string | null`, `policies.subject: string`
  (default `"context"`) — consumed by every later task.

- [ ] **Step 1: Add the columns**

In `src/infra/db/schema.ts`, change the `policies` table definition from:

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

to:

```ts
export const policies = pgTable(
  "policies",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    action: varchar("action", { length: 20 }).notNull(),
    field: varchar("field", { length: 120 }),
    subject: varchar("subject", { length: 20 }).notNull().default("context"),
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

- [ ] **Step 2: Generate and apply the migration**

Run: `pnpm db:generate`
Expected: a new migration with `ALTER TABLE "policies" ADD COLUMN "field" ...` and `ADD COLUMN "subject" ... DEFAULT 'context' NOT NULL`. Open it, confirm no unrelated diffs.

Run: `pnpm db:migrate`
Expected: exits 0.

- [ ] **Step 3: Verify**

Run: `docker compose exec postgres psql -U metap -d metap -c '\d policies'`
Expected: 10 columns now, `field` nullable, `subject` not-null with default `'context'::character varying`.

---

### Task 2: `roleGatePassed` — shared role-gate primitive

**Files:**
- Modify: `src/core/permission/policy-condition.ts`
- Modify: `src/core/permission/policy-condition.test.ts`

**Interfaces:**
- Produces: `roleGatePassed(policyRoles: readonly string[] | null, callerRoles: readonly string[] | undefined): boolean` — consumed by Task 5's `PermissionService` and Task 3's `condition-to-sql.ts`.

- [ ] **Step 1: Write the failing tests**

Append to `src/core/permission/policy-condition.test.ts` (add the import at
the top alongside the existing ones, and the new `describe` block at the
end of the file):

```ts
import { evaluateCondition, roleGatePassed } from "./policy-condition";
```

(replaces the existing `import { evaluateCondition } from "./policy-condition";` line)

```ts

describe("roleGatePassed", () => {
  it("passes when the policy has no role restriction", () => {
    expect(roleGatePassed(null, ["viewer"])).toBe(true);
    expect(roleGatePassed([], ["viewer"])).toBe(true);
  });

  it("passes when the caller has one of the listed roles", () => {
    expect(roleGatePassed(["editor", "viewer"], ["viewer"])).toBe(true);
  });

  it("fails when the caller has none of the listed roles", () => {
    expect(roleGatePassed(["editor"], ["viewer"])).toBe(false);
    expect(roleGatePassed(["editor"], undefined)).toBe(false);
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/policy-condition.test.ts`
Expected: FAIL — `roleGatePassed` is not exported yet.

- [ ] **Step 3: Implement `roleGatePassed`**

In `src/core/permission/policy-condition.ts`, add this function (anywhere
before `evaluateCondition`, e.g. right after `matchOperator`):

```ts
export function roleGatePassed(
  policyRoles: readonly string[] | null,
  callerRoles: readonly string[] | undefined,
): boolean {
  if (!policyRoles || policyRoles.length === 0) {
    return true;
  }
  return (callerRoles ?? []).some((role) => policyRoles.includes(role));
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/policy-condition.test.ts`
Expected: PASS (11 tests: 8 existing + 3 new).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 3: `condition-to-sql.ts` — translate `PolicyCondition` to Drizzle SQL

**Files:**
- Create: `src/core/query/condition-to-sql.ts`

**Interfaces:**
- Consumes: `PolicyCondition`, `roleGatePassed` (Task 2), `PolicyRow` (Task 5 — see note below on ordering).
- Produces (consumed by Task 4's `QueryPlanner`):
  - `conditionToSql(condition: PolicyCondition, context: RequestContext): SQL`
  - `recordPolicyWhereClause(rows: PolicyRow[], context: RequestContext): SQL | undefined`

Note on ordering: this task references `PolicyRow`, which Task 5 defines.
Since `PolicyRow` is just `typeof policies.$inferSelect` (a type derived
directly from the schema table, Task 1), it's fine to import it from
`permission-service.ts` here even though Task 5 (which adds the `export`)
comes after this task in the file list — add `export type PolicyRow =
typeof policies.$inferSelect;` to `permission-service.ts` as part of
**this** task's Step 1 (a one-line addition), not Task 5's, to keep the
dependency direction correct. Task 5 will find it already there.

- [ ] **Step 1: Export `PolicyRow` from `permission-service.ts`**

In `src/core/permission/permission-service.ts`, add this line right after
the `import` block (before `export type RequestContext`):

```ts
export type PolicyRow = typeof policies.$inferSelect;
```

- [ ] **Step 2: Implement `condition-to-sql.ts`**

Create `src/core/query/condition-to-sql.ts`:

```ts
import type { SQL } from "drizzle-orm";
import { and, eq, inArray, ne, notInArray, or, sql } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { PolicyCondition, PolicyValue } from "../permission/policy-condition";
import { roleGatePassed } from "../permission/policy-condition";
import type { PolicyRow, RequestContext } from "../permission/permission-service";

function fieldExpression(fieldName: string) {
  if (fieldName === "createdBy") {
    return records.createdBy;
  }
  if (fieldName === "updatedBy") {
    return records.updatedBy;
  }
  if (fieldName === "status") {
    return records.status;
  }
  if (fieldName === "createdAt") {
    return records.createdAt;
  }
  if (fieldName === "updatedAt") {
    return records.updatedAt;
  }
  return sql`jsonb_extract_path_text(${records.data}, ${fieldName})`;
}

function resolveValue(value: PolicyValue, context: RequestContext): unknown {
  return "literal" in value ? value.literal : context[value.fromContext];
}

export function conditionToSql(condition: PolicyCondition, context: RequestContext): SQL {
  if ("all" in condition) {
    const clauses = condition.all.map((inner) => conditionToSql(inner, context));
    return clauses.length > 0 ? and(...clauses)! : sql`true`;
  }

  if ("any" in condition) {
    const clauses = condition.any.map((inner) => conditionToSql(inner, context));
    return clauses.length > 0 ? or(...clauses)! : sql`false`;
  }

  const expr = fieldExpression(condition.attribute);
  const expected = resolveValue(condition.value, context);

  switch (condition.op) {
    case "eq":
      return eq(expr, expected);
    case "neq":
      return ne(expr, expected);
    case "in":
      return inArray(expr, Array.isArray(expected) ? expected : []);
    case "notIn":
      return notInArray(expr, Array.isArray(expected) ? expected : []);
  }
}

export function recordPolicyWhereClause(
  rows: PolicyRow[],
  context: RequestContext,
): SQL | undefined {
  if (rows.length === 0) {
    return undefined;
  }

  const passingRows = rows.filter((row) =>
    roleGatePassed(row.roles as string[] | null, context.roles),
  );

  if (passingRows.length === 0) {
    return sql`false`;
  }

  const clauses = passingRows.map((row) => {
    const condition = row.condition as PolicyCondition | null;
    return condition ? conditionToSql(condition, context) : sql`true`;
  });

  return or(...clauses);
}
```

`recordPolicyWhereClause` embodies the OR-across-policies semantics
established in sub-project 2, adapted for SQL: no record-level policies at
all → no restriction (`undefined`, matches default-open); policies exist
but none match the caller's role → deny everything (`sql\`false\``, since
this policy set was clearly meant to restrict and the caller qualifies for
none of it); otherwise, OR the passing policies' conditions (a policy with
no condition, once its role gate passes, means "always allowed" —
contributes `sql\`true\`` to the OR, which is deliberate).

- [ ] **Step 3: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors (Task 5 hasn't added the rest of `PolicyRow`'s
consumers yet, but the type itself now exists).

---

### Task 4: Wire record-level read policies into `QueryPlanner`

**Files:**
- Modify: `src/core/query/query-planner.ts` (full file)
- Modify: `src/core/query/query-planner.test.ts` (append tests)

**Interfaces:**
- Consumes: `recordPolicyWhereClause` (Task 3).
- Produces: `QueryPlanner.planList`'s new 4th parameter,
  `recordReadPolicies: PolicyRow[] = []` — consumed by Task 6's
  `CrudService.list`.

- [ ] **Step 1: Update `planList`'s signature and WHERE construction**

Replace the full contents of `src/core/query/query-planner.ts` with:

```ts
import type { SQL } from "drizzle-orm";
import { and, asc, desc, eq, sql } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { PermissionService, PolicyRow, RequestContext } from "../permission/permission-service";
import { recordPolicyWhereClause } from "./condition-to-sql";

export type ListInput = {
  limit: number;
  sort?: string;
  filters?: Record<string, string>;
};

export type PlannedListQuery = {
  where: SQL | undefined;
  limit: number;
  orderBy: SQL[];
};

type ResolvedSort = { field: string; descending: boolean };

function parseSort(
  candidate: string | undefined,
  sortableFields: ReadonlySet<string>,
): ResolvedSort | undefined {
  if (!candidate) {
    return undefined;
  }

  const descending = candidate.startsWith("-");
  const field = descending ? candidate.slice(1) : candidate;

  return sortableFields.has(field) ? { field, descending } : undefined;
}

function fieldExpression(fieldName: string) {
  if (fieldName === "createdAt") {
    return records.createdAt;
  }
  if (fieldName === "updatedAt") {
    return records.updatedAt;
  }
  return sql`jsonb_extract_path_text(${records.data}, ${fieldName})`;
}

export class QueryPlanner {
  constructor(
    private readonly metadata: MetadataRegistry,
    private readonly permissions: PermissionService,
  ) {}

  planList(
    entityName: string,
    input: ListInput,
    context: Partial<RequestContext>,
    recordReadPolicies: PolicyRow[] = [],
  ): PlannedListQuery {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      throw new Error(`Entity not found: ${entityName}`);
    }

    const tenantId = this.permissions.scopedTenant(context);
    const listView = entity.listViews[0];
    const limit = Math.min(input.limit, listView?.maxLimit ?? 100);

    const conditions: SQL[] = [
      eq(records.tenantId, tenantId),
      eq(records.entity, entity.name),
      eq(records.deleted, false),
    ];

    const recordCondition = recordPolicyWhereClause(recordReadPolicies, context as RequestContext);
    if (recordCondition) {
      conditions.push(recordCondition);
    }

    const allowedFilterFields = new Set(listView?.filters ?? []);
    const fieldsByName = new Map(entity.fields.map((field) => [field.name, field]));

    for (const [field, value] of Object.entries(input.filters ?? {})) {
      if (!allowedFilterFields.has(field)) {
        continue;
      }

      const fieldExpr = fieldExpression(field);
      const fieldDef = fieldsByName.get(field);

      if (fieldDef?.searchable) {
        const escapedValue = value.replace(/[\\%_]/g, "\\$&");
        conditions.push(sql`${fieldExpr} ILIKE ${`%${escapedValue}%`}`);
      } else {
        conditions.push(sql`${fieldExpr} = ${value}`);
      }
    }

    const sortableFields = new Set<string>([
      ...entity.fields.filter((field) => field.sortable).map((field) => field.name),
      "createdAt",
      "updatedAt",
    ]);

    const resolvedSort = parseSort(input.sort, sortableFields) ??
      parseSort(listView?.defaultSort, sortableFields) ?? { field: "createdAt", descending: true };

    const sortExpr = fieldExpression(resolvedSort.field);

    return {
      where: and(...conditions),
      limit,
      orderBy: [resolvedSort.descending ? desc(sortExpr) : asc(sortExpr), asc(records.id)],
    };
  }
}
```

(Only changes from the current file: the new `recordReadPolicies`
parameter with a default of `[]`, and the 5 lines that compute and push
`recordCondition`. `context as RequestContext` is a pragmatic cast — every
real caller passes a full context; the `Partial<RequestContext>` parameter
type exists for `scopedTenant`'s flexibility, unrelated to this new code
path.)

- [ ] **Step 2: Run the existing tests to verify nothing broke**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: PASS (all existing tests — the new parameter defaults to `[]`,
which `recordPolicyWhereClause` turns into `undefined`, i.e. no behavior
change for callers that don't pass it).

- [ ] **Step 3: Write the failing tests for record-level filtering**

Append to `src/core/query/query-planner.test.ts`, inside the existing
`describe("QueryPlanner (via CrudService.list, live DB)", ...)` block,
after the last `it(...)` and before the closing `});`:

```ts

  it("filters list() results by a record-level read policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const nonAdminContext: RequestContext = {
      tenantId: context.tenantId,
      userId: "00000000-0000-0000-0000-000000000032",
      roles: ["viewer"],
    };

    const policy = await container.permissions.createPolicy(
      context.tenantId,
      "crm.customers",
      "read",
      undefined,
      { attribute: "status", op: "eq", value: { literal: "active" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list(
        "crm.customers",
        { limit: 30 },
        nonAdminContext,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        const statuses = result.data.map((record) => (record.data as { status?: string }).status);
        expect(statuses.every((status) => status === "active")).toBe(true);
        expect(statuses.length).toBe(2);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(context.tenantId, policy.id);
      }
    }
  });

  it("denies all rows when a record-level read policy's role gate matches no one", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const nonAdminContext: RequestContext = {
      tenantId: context.tenantId,
      userId: "00000000-0000-0000-0000-000000000033",
      roles: ["viewer"],
    };

    const policy = await container.permissions.createPolicy(
      context.tenantId,
      "crm.customers",
      "read",
      ["nobody-has-this-role"],
      undefined,
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list("crm.customers", { limit: 30 }, nonAdminContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.length).toBe(0);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(context.tenantId, policy.id);
      }
    }
  });
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: FAIL — `container.crud.list` doesn't yet fetch or apply
record-level policies (Task 6 wires that up); both new tests should see
all 3 seeded rows instead of the filtered subset.

(This step will only truly go green after Task 6 — that's expected. Note
it here as red, then return to confirm green at the end of Task 6's
verification.)

---

### Task 5: `PermissionService` — field/record policy methods

**Files:**
- Modify: `src/core/permission/permission-service.ts` (full file)
- Modify: `src/core/permission/permission-service.test.ts` (append tests)

**Interfaces:**
- Consumes: `roleGatePassed` (Task 2), `PolicyRow` (already exported in
  Task 3, Step 1).
- Produces (consumed by Task 6's `CrudService`):
  - `getFieldPolicies(tenantId, entity): Promise<PolicyRow[]>`
  - `getRecordPolicies(tenantId, entity, action): Promise<PolicyRow[]>`
  - `filterReadableFields(context, record, fieldPolicies): Record<string, unknown>`
  - `assertWritableFields(context, payloadFields, existingRecord, fieldPolicies): PermissionDecision`
  - `canUpdateRecordCondition(context, record, recordPolicies): PermissionDecision`
  - `createPolicy(...)` gains two new optional trailing parameters:
    `field?: string`, `subject?: "context" | "record"`.

- [ ] **Step 1: Write the failing tests**

Append to `src/core/permission/permission-service.test.ts`, inside the
existing `describe("PermissionService (live DB)", ...)` block, after the
last `it(...)` and before the closing `});`:

```ts

  it("filterReadableFields strips a field the caller cannot read", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        ["hr"],
        undefined,
        undefined,
        "salary",
      );

      const fieldPolicies = await service.getFieldPolicies(tenantId, entity);
      const record = { name: "Alice", salary: 100000 };

      const asHr = service.filterReadableFields(contextWithRoles(["hr"]), record, fieldPolicies);
      expect(asHr).toEqual({ name: "Alice", salary: 100000 });

      const asViewer = service.filterReadableFields(
        contextWithRoles(["viewer"]),
        record,
        fieldPolicies,
      );
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

      const fieldPolicies = await service.getFieldPolicies(tenantId, entity);

      const activeRecord = { status: "active", internalNotes: "secret" };
      const draftRecord = { status: "draft", internalNotes: "secret" };

      expect(
        service.filterReadableFields(contextWithRoles(["viewer"]), activeRecord, fieldPolicies),
      ).toEqual(activeRecord);
      expect(
        service.filterReadableFields(contextWithRoles(["viewer"]), draftRecord, fieldPolicies),
      ).toEqual({ status: "draft" });
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
      await service.createPolicy(
        tenantId,
        entity,
        "write",
        ["hr"],
        undefined,
        undefined,
        "salary",
      );

      const fieldPolicies = await service.getFieldPolicies(tenantId, entity);

      const allowed = service.assertWritableFields(
        contextWithRoles(["hr"]),
        ["name", "salary"],
        undefined,
        fieldPolicies,
      );
      expect(allowed.allowed).toBe(true);

      const denied = service.assertWritableFields(
        contextWithRoles(["viewer"]),
        ["name", "salary"],
        undefined,
        fieldPolicies,
      );
      expect(denied.allowed).toBe(false);
      expect(denied.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("canUpdateRecordCondition evaluates against the record, not context", async (ctx) => {
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

      const recordPolicies = await service.getRecordPolicies(tenantId, entity, "update");
      const callerContext = contextWithRoles(["editor"], { userId: "user-1" });

      const owned = service.canUpdateRecordCondition(
        callerContext,
        { createdBy: "user-1" },
        recordPolicies,
      );
      expect(owned.allowed).toBe(true);

      const notOwned = service.canUpdateRecordCondition(
        callerContext,
        { createdBy: "someone-else" },
        recordPolicies,
      );
      expect(notOwned.allowed).toBe(false);
    } finally {
      await cleanup();
    }
  });

  it("checkAction ignores field-scoped policies when checking entity-level actions", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      // A field-scoped "read" policy that would deny 'viewer' if (incorrectly)
      // picked up by the entity-level check.
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        ["someone-else"],
        undefined,
        undefined,
        "salary",
      );

      const decision = await service.canReadEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: FAIL — the new methods don't exist, and `createPolicy` doesn't
accept a `field` argument yet.

- [ ] **Step 3: Implement the new `PermissionService` methods**

Replace the full contents of `src/core/permission/permission-service.ts`
with:

```ts
import { and, eq, isNotNull, isNull } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluateCondition, roleGatePassed } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";

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

  private evaluatePolicyRow(
    context: RequestContext,
    policy: PolicyRow,
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
        ),
      );

    if (rows.length === 0) {
      return { allowed: true };
    }

    const passed = rows.some((policy) => this.evaluatePolicyRow(context, policy, undefined));

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

  async listPolicies(tenantId: string, entity?: string) {
    const where = entity
      ? and(eq(policies.tenantId, tenantId), eq(policies.entity, entity))
      : eq(policies.tenantId, tenantId);

    return this.db.client.select().from(policies).where(where);
  }

  async getFieldPolicies(tenantId: string, entity: string): Promise<PolicyRow[]> {
    return this.db.client
      .select()
      .from(policies)
      .where(
        and(eq(policies.tenantId, tenantId), eq(policies.entity, entity), isNotNull(policies.field)),
      );
  }

  async getRecordPolicies(
    tenantId: string,
    entity: string,
    action: EntityAction,
  ): Promise<PolicyRow[]> {
    return this.db.client
      .select()
      .from(policies)
      .where(
        and(
          eq(policies.tenantId, tenantId),
          eq(policies.entity, entity),
          eq(policies.action, action),
          isNull(policies.field),
          eq(policies.subject, "record"),
        ),
      );
  }

  filterReadableFields(
    context: RequestContext,
    record: Record<string, unknown>,
    fieldPolicies: PolicyRow[],
  ): Record<string, unknown> {
    if (context.roles?.includes("admin")) {
      return record;
    }

    const readPoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of fieldPolicies) {
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

      const passed = fieldReadPolicies.some((policy) =>
        this.evaluatePolicyRow(context, policy, record),
      );

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
    fieldPolicies: PolicyRow[],
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const writePoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of fieldPolicies) {
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
        this.evaluatePolicyRow(context, policy, existingRecord),
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
    recordPolicies: PolicyRow[],
  ): PermissionDecision {
    if (context.roles?.includes("admin") || recordPolicies.length === 0) {
      return { allowed: true };
    }

    const passed = recordPolicies.some((policy) => this.evaluatePolicyRow(context, policy, record));

    return passed ? { allowed: true } : { allowed: false, reason: "forbidden" };
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
}
```

Notes on this rewrite versus the current file:

- `checkAction`'s query gains `isNull(policies.field)` — the correctness
  fix described in Global Constraints.
- `checkAction`'s per-policy loop is replaced by the shared
  `evaluatePolicyRow` helper (calling it with `recordSubject: undefined`,
  since entity-level actions have no record in scope) — behavior is
  unchanged from sub-project 2, this is a refactor to share logic with the
  new field/record methods.
- `action`'s type on `createPolicy` widens from `EntityAction` to `string`
  — field policies use `"write"`, which isn't a member of `EntityAction`.
  Validation of the actual allowed values per context (entity-level vs.
  field-level) happens at the Zod layer in `admin.ts` (Task 7), not here.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: PASS (12 tests: 7 existing + 5 new).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: errors in `src/server/routes/admin.ts` (its `createPolicy` call
site doesn't pass the two new params yet, but that's fine since they're
optional — actually expect **no** error there) and no new errors anywhere.
If `admin.ts` does show an error, it means a mismatch versus this task's
`createPolicy` signature — stop and recheck before continuing to Task 6.

---

### Task 6: Wire enforcement into `CrudService`

**Files:**
- Modify: `src/core/crud/crud-service.ts` (full file)
- Modify: `src/core/crud/crud-service.test.ts` (append tests)

**Interfaces:**
- Consumes: `PermissionService`'s new methods (Task 5), `QueryPlanner.planList`'s new parameter (Task 4).

- [ ] **Step 1: Write the failing tests**

Append to `src/core/crud/crud-service.test.ts`, inside the existing
`describe("CrudService.transition (live DB)", ...)` block's sibling scope
— i.e. as a **new top-level `describe`** at the end of the file (after
that block's closing `});`), since it needs its own policy cleanup and a
non-admin context distinct from the other suites' fixtures:

```ts

describe("CrudService field/record enforcement (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000070";
  const adminContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000071",
    roles: ["admin"],
  };
  const editorContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000072",
    roles: ["editor"],
  };
  const viewerContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000073",
    roles: ["viewer"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-enforcement-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    container = createContainer(config);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping CrudService enforcement live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("masks a field the caller cannot read from create/update responses", async (ctx) => {
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
        { code: "E001", name: "Enforcement Co", phone: "555-1000" },
        viewerContext,
      );

      expect(created.ok).toBe(true);
      if (created.ok) {
        recordId = created.data.id;
        expect((created.data.data as { phone?: string }).phone).toBeUndefined();
        expect((created.data.data as { name?: string }).name).toBe("Enforcement Co");
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

  it("rejects a create payload touching a field the caller cannot write", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "write",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    try {
      const result = await container.crud.create(
        "crm.customers",
        { code: "E002", name: "Blocked Co", phone: "555-2000" },
        viewerContext,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
    }
  });

  it("rejects updating a record that fails a record-level condition", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "E003", name: "Owned Co" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "update",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Hijacked" },
        editorContext,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: FAIL — the 3 new tests fail (masking/rejection not wired yet);
all pre-existing tests in this file still pass.

- [ ] **Step 3: Wire enforcement into `list`, `create`, `update`, `transition`**

Replace the full contents of `src/core/crud/crud-service.ts` with:

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

    const recordPolicies = await this.permissions.getRecordPolicies(
      context.tenantId,
      entity.name,
      "read",
    );
    const fieldPolicies = await this.permissions.getFieldPolicies(context.tenantId, entity.name);

    const plan = this.queryPlanner.planList(entity.name, input, context, recordPolicies);
    const rows = await this.db.client
      .select()
      .from(records)
      .where(plan.where)
      .orderBy(...plan.orderBy)
      .limit(plan.limit);

    const data = rows.map((row) => ({
      ...row,
      data: this.permissions.filterReadableFields(
        context,
        row.data as Record<string, unknown>,
        fieldPolicies,
      ),
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

    const fieldPolicies = await this.permissions.getFieldPolicies(context.tenantId, entity.name);
    const writeDecision = this.permissions.assertWritableFields(
      context,
      Object.keys(rawData),
      undefined,
      fieldPolicies,
    );

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
        data: this.permissions.filterReadableFields(
          context,
          outcome.record.data as Record<string, unknown>,
          fieldPolicies,
        ),
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

    const recordPolicies = await this.permissions.getRecordPolicies(
      context.tenantId,
      entity.name,
      "update",
    );
    const recordDecision = this.permissions.canUpdateRecordCondition(
      context,
      existingData,
      recordPolicies,
    );

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    const fieldPolicies = await this.permissions.getFieldPolicies(context.tenantId, entity.name);
    const writeDecision = this.permissions.assertWritableFields(
      context,
      Object.keys(rawData),
      existingData,
      fieldPolicies,
    );

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
        data: this.permissions.filterReadableFields(
          context,
          outcome.record.data as Record<string, unknown>,
          fieldPolicies,
        ),
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

    const recordPolicies = await this.permissions.getRecordPolicies(
      context.tenantId,
      entity.name,
      "update",
    );
    const recordDecision = this.permissions.canUpdateRecordCondition(
      context,
      existingData,
      recordPolicies,
    );

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

    const fieldPolicies = await this.permissions.getFieldPolicies(context.tenantId, entity.name);

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: this.permissions.filterReadableFields(
          context,
          outcome.record.data as Record<string, unknown>,
          fieldPolicies,
        ),
      },
    };
  }

  async flushOutbox() {
    await this.outbox.publishPending();
  }
}
```

Key ordering decisions in this rewrite:

- `update`/`transition`: record-level check happens right after fetching
  the existing record, **before** field-write checks and before
  transition's workflow-validity checks — it's an authorization gate, so
  it takes priority over business-logic checks like `no_workflow`/
  `invalid_transition`/`guard_failed`.
- `create`: field-write check happens **before** `entity.schema.safeParse`,
  using `Object.keys(rawData)` (raw payload keys, not Zod-defaulted keys)
  — see Global Constraints.
- `transition` has no field-write check — a transition only ever moves the
  workflow state field, which isn't caller-suppliable payload, so there's
  nothing for a field-write policy to gate here.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: PASS (all tests: the pre-existing ones from Workflow Engine V1
plus the 3 new enforcement tests).

- [ ] **Step 5: Confirm Task 4's record-level list tests now pass**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: PASS (all tests, including the two record-level filtering tests
that were red at the end of Task 4 — they depend on `CrudService.list`'s
wiring, which this task just added).

- [ ] **Step 6: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 7: Admin API — accept `field`/`subject`, and the `"write"` action

**Files:**
- Modify: `src/server/routes/admin.ts`
- Modify: `src/server/routes/admin.test.ts` (append tests)

**Interfaces:**
- Consumes: `PermissionService.createPolicy`'s new parameters (Task 5).

- [ ] **Step 1: Write the failing tests**

Append to `src/server/routes/admin.test.ts`, inside the existing
`describe("admin routes (live DB)", ...)` block, before the closing
`});`:

```ts

  it("creates a field-scoped policy with subject 'record'", async (ctx) => {
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
        action: "write",
        field: "phone",
        subject: "record",
        condition: { attribute: "status", op: "eq", value: { literal: "draft" } },
      },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{
      data: { id: string; field: string; subject: string; action: string };
    }>();
    expect(created.data.field).toBe("phone");
    expect(created.data.subject).toBe("record");
    expect(created.data.action).toBe("write");

    await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: FAIL — `action: "write"` isn't in the current Zod enum, so the
request 400s.

- [ ] **Step 3: Update `CreatePolicyBodySchema` and the create-policy route**

In `src/server/routes/admin.ts`, change:

```ts
const CreatePolicyBodySchema = z.object({
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update"]),
  roles: z.array(z.string()).optional(),
  condition: PolicyConditionSchema.optional(),
});
```

to:

```ts
const CreatePolicyBodySchema = z.object({
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update", "write"]),
  roles: z.array(z.string()).optional(),
  condition: PolicyConditionSchema.optional(),
  field: z.string().optional(),
  subject: z.enum(["context", "record"]).optional(),
});
```

Then change the `POST /admin/policies` handler's `createPolicy` call from:

```ts
      const created = await container.permissions.createPolicy(
        request.context.tenantId,
        body.entity,
        body.action,
        body.roles,
        body.condition as PolicyCondition | undefined,
        request.context.userId,
      );
```

to:

```ts
      const created = await container.permissions.createPolicy(
        request.context.tenantId,
        body.entity,
        body.action,
        body.roles,
        body.condition as PolicyCondition | undefined,
        request.context.userId,
        body.field,
        body.subject,
      );
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: PASS (all tests: sub-project 2's 7 plus this new one).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 8: Full verification + manual end-to-end check

**Files:** none (verification only).

- [ ] **Step 1: Full test suite**

Run: `pnpm test`
Expected: every test file passes, including all of sub-projects 1-2's
suites unmodified (no field/record policies exist for `crm.customers` by
default, so every new code path's "no policies" branch is a no-op).

- [ ] **Step 2: Typecheck and lint**

Run: `pnpm typecheck`
Expected: no new errors.

Run: `pnpm eslint src/infra/db/schema.ts src/core/permission/policy-condition.ts src/core/permission/policy-condition.test.ts src/core/query/condition-to-sql.ts src/core/query/query-planner.ts src/core/query/query-planner.test.ts src/core/permission/permission-service.ts src/core/permission/permission-service.test.ts src/core/crud/crud-service.ts src/core/crud/crud-service.test.ts src/server/routes/admin.ts src/server/routes/admin.test.ts`
Expected: any `@typescript-eslint/no-unsafe-*` warnings on jsonb-column
casts (`policy.roles as string[] | null`, `policy.condition as
PolicyCondition | null`, `existing.data as Record<string, unknown>`) match
the class of warning already accepted as pre-existing elsewhere in this
codebase — not a regression to fix. Everything else should be clean; fix
anything that isn't.

- [ ] **Step 3: Manual verification against the dev server**

Start `pnpm dev` if not already running, then:

```bash
pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
ADMIN_TOKEN=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002)

# field-level: hide "phone" from everyone except admin
curl -s -X POST http://localhost:3000/admin/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","action":"read","field":"phone","roles":["admin"]}'

# create a record as admin with a phone number
CUSTOMER=$(curl -s -X POST http://localhost:3000/api/crm.customers \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"FR1","name":"Field Record Test","phone":"555-9999"}}')
echo "$CUSTOMER"
# expect: response includes "phone":"555-9999" (admin can read it)

# list as a non-admin user — phone should be absent from every row
OTHER_TOKEN=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000097)
curl -s "http://localhost:3000/api/crm.customers?limit=30" -H "Authorization: Bearer $OTHER_TOKEN"
# expect: none of the returned records include a "phone" key
```

Confirm both expectations hold, then clean up:

```bash
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM policies WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM user_roles WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM records WHERE code = 'FR1');"
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM records WHERE code = 'FR1';"
```

Stop the dev server afterward if you started a new one for this.

---

## Plan Self-Review Notes

- **Spec coverage:** §1 (schema) → Task 1. §2 (`conditionToSql`) → Task 3. §3 (`PermissionService` methods) → Task 5. §4 (`CrudService` wiring) → Task 6. §5 (admin API) → Task 7. "Consequences for existing code" → Tasks 4-7 each note their own backward-compatibility. Open items (exact migration, exact SQL mapping, merged-shape confirmation) are all resolved concretely in the tasks above, not left open.
- **No placeholders:** every step has literal code.
- **Type consistency checked:** `PolicyRow` (Task 3, Step 1) is used identically across `condition-to-sql.ts` (Task 3), `query-planner.ts` (Task 4), and `permission-service.ts` (Task 5). `QueryPlanner.planList`'s 4th parameter type/default (Task 4) matches its call site in `CrudService.list` (Task 6). `PermissionService.createPolicy`'s two new trailing optional parameters (Task 5) match their call site in `admin.ts` (Task 7) exactly, and match every test call site added across Tasks 4-7 (positional order: `field` before `subject`, both after `createdBy`).
- **Ordering dependency flagged inline:** Task 3 needs `PolicyRow`, which is conceptually "owned" by Task 5's rewrite of `permission-service.ts` — resolved by having Task 3 add the one-line export itself (noted explicitly in Task 3, Step 1) rather than silently assuming it already exists.
