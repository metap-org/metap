# PermissionService: Entity-Level RBAC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `PermissionService`'s allow-everything stubs with a real, minimal entity-level RBAC check: whether the caller's role(s) may read/create/update a given entity type.

**Architecture:** `EntityDefinition` gains an optional `permissions` block (role lists per action). `PermissionService` gains a `MetadataRegistry` constructor dependency (mirroring `QueryPlanner`'s existing pattern) so it can look up an entity's declared permissions by name — `CrudService`'s call sites don't change. `admin` role bypasses unconditionally; an entity with no `permissions` declared (like `crm.customers` today) allows any role, unchanged from current behavior.

**Tech Stack:** TypeScript, Zod (test fixture only), Vitest.

## Global Constraints

- **CLAUDE.md now says: do not commit any changes — leave the diff intact for the user to review.** Every step below that would normally say "commit" instead says to leave the change in the working tree. Do not run `git commit` at any point in this plan.
- Entity-level RBAC only — no field-level, no record-level, no ABAC beyond role membership, no policy simulator, no permission cache. All explicitly out of scope.
- `admin` role is a hardcoded, unconditional bypass for every action on every entity — not itself declared anywhere, not overridable per-entity.
- An entity with no `permissions` block, or no entry for a given action, allows **any** role for that action (matches current allow-everything behavior — no breaking change for `crm.customers`, which stays undeclared).
- Denial returns `{ allowed: false, reason: "forbidden" }` explicitly.
- Do NOT add a `permissions` block to `src/modules/crm/customer.entity.ts` — the restriction path is tested with a throwaway test-only `EntityDefinition`, not by changing real production entity config.
- Nothing in this change touches the database. Tests are pure unit tests against a hand-built `MetadataRegistry`, not live-DB integration tests — no `docker compose`/live Postgres needed for this plan at all.
- Testing is scoped to exactly 4 cases (admin bypass, no-permissions-declared allows any role, allowed role passes, disallowed role is denied) — no exhaustive matrix.

---

### Task 1: `EntityDefinition.permissions` + `PermissionService` real RBAC + tests

**Files:**
- Modify: `src/core/metadata/entity.ts`
- Modify: `src/core/permission/permission-service.ts`
- Modify: `src/core/container.ts`
- Create: `src/core/permission/permission-service.test.ts`

**Interfaces:**
- Produces: `EntityPermissions = { read?: readonly string[]; create?: readonly string[]; update?: readonly string[] }`, added as `EntityDefinition.permissions?: EntityPermissions`. `PermissionService` constructor becomes `constructor(private readonly metadata: MetadataRegistry)`. `canReadEntity`/`canCreateEntity`/`canUpdateEntity` keep their existing signatures (`(context: RequestContext, entity: string) => PermissionDecision`) — only their internal behavior changes.
- Consumes: `MetadataRegistry` (existing, `src/core/metadata/metadata-registry.ts`) — `getEntity(name)` returns `EntityDefinition | undefined`.

- [ ] **Step 1: Write the failing tests, `src/core/permission/permission-service.test.ts`**

```ts
import { z } from "zod";
import { describe, expect, it } from "vitest";
import type { EntityDefinition } from "../metadata/entity";
import { MetadataRegistry } from "../metadata/metadata-registry";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";

const TestEntitySchema = z.object({ name: z.string() });

const restrictedEntity: EntityDefinition<typeof TestEntitySchema> = {
  name: "test.restricted",
  label: "Restricted Test Entity",
  tableName: "records",
  schema: TestEntitySchema,
  fields: [{ name: "name", label: "Name", kind: "string" }],
  listViews: [
    {
      name: "default",
      label: "Default",
      fields: ["name"],
      filters: [],
      maxLimit: 100,
    },
  ],
  permissions: {
    read: ["viewer", "editor"],
    create: ["editor"],
    update: ["editor"],
  },
};

const openEntity: EntityDefinition<typeof TestEntitySchema> = {
  name: "test.open",
  label: "Open Test Entity",
  tableName: "records",
  schema: TestEntitySchema,
  fields: [{ name: "name", label: "Name", kind: "string" }],
  listViews: [
    {
      name: "default",
      label: "Default",
      fields: ["name"],
      filters: [],
      maxLimit: 100,
    },
  ],
};

function buildService() {
  const metadata = new MetadataRegistry();
  metadata.register(restrictedEntity);
  metadata.register(openEntity);
  return new PermissionService(metadata);
}

function contextWithRoles(roles: string[]): RequestContext {
  return { tenantId: "00000000-0000-0000-0000-000000000001", roles };
}

describe("PermissionService", () => {
  it("allows admin regardless of the entity's declared permissions", () => {
    const permissions = buildService();
    const decision = permissions.canCreateEntity(contextWithRoles(["admin"]), "test.restricted");
    expect(decision.allowed).toBe(true);
  });

  it("allows any role when the entity declares no permissions", () => {
    const permissions = buildService();
    const decision = permissions.canReadEntity(
      contextWithRoles(["nobody-in-particular"]),
      "test.open",
    );
    expect(decision.allowed).toBe(true);
  });

  it("allows a role that is in the entity's allowed list", () => {
    const permissions = buildService();
    const decision = permissions.canReadEntity(contextWithRoles(["viewer"]), "test.restricted");
    expect(decision.allowed).toBe(true);
  });

  it("denies a role that is not in the entity's allowed list", () => {
    const permissions = buildService();
    const decision = permissions.canCreateEntity(contextWithRoles(["viewer"]), "test.restricted");
    expect(decision.allowed).toBe(false);
    expect(decision.reason).toBe("forbidden");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: FAIL — a TypeScript error, since `PermissionService`'s constructor doesn't accept a `MetadataRegistry` argument yet and `EntityDefinition` has no `permissions` field.

- [ ] **Step 3: Add `EntityPermissions` to `src/core/metadata/entity.ts`**

Add this type, and add the new field to `EntityDefinition`:

```ts
export type EntityPermissions = {
  read?: readonly string[];
  create?: readonly string[];
  update?: readonly string[];
};
```

Change the `EntityDefinition` type to:

```ts
export type EntityDefinition<TInput extends z.ZodTypeAny = z.ZodTypeAny> = {
  name: string;
  label: string;
  tableName: string;
  schema: TInput;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: EntityWorkflow;
  permissions?: EntityPermissions;
};
```

- [ ] **Step 4: Rewrite `src/core/permission/permission-service.ts`**

Replace the entire file content with:

```ts
import type { MetadataRegistry } from "../metadata/metadata-registry";

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

type EntityAction = "read" | "create" | "update";

export class PermissionService {
  constructor(private readonly metadata: MetadataRegistry) {}

  private checkAction(
    context: RequestContext,
    entityName: string,
    action: EntityAction,
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const entity = this.metadata.getEntity(entityName);
    const allowedRoles = entity?.permissions?.[action];

    if (!allowedRoles) {
      return { allowed: true };
    }

    const callerRoles = context.roles ?? [];
    const hasAllowedRole = callerRoles.some((role) => allowedRoles.includes(role));

    return hasAllowedRole ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }

  canReadEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "read");
  }

  canCreateEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "create");
  }

  canUpdateEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "update");
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }
}
```

- [ ] **Step 5: Wire the new dependency in `src/core/container.ts`**

Change:

```ts
  const permissions = new PermissionService();
```

to:

```ts
  const permissions = new PermissionService(metadata);
```

`metadata` is already constructed and populated (`metadata.register(customerEntity)`) earlier in this same function, before this line — no reordering needed.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/permission/permission-service.test.ts`
Expected: 4 passed. No live Postgres needed for this file.

- [ ] **Step 7: Run the full test suite and typecheck**

Run: `pnpm vitest run && pnpm typecheck`
Expected: full suite passing (bring up `docker compose up -d postgres rabbitmq` + `pnpm db:migrate` first if not already running, so the other, pre-existing live-DB suites still exercise the DB rather than skipping — this task's own new tests don't need it, but don't let the rest of the suite silently skip either). Typecheck: only the one known pre-existing error (`src/infra/messaging/rabbitmq.ts`), zero new — in particular, confirm `src/core/crud/crud-service.ts`'s existing calls (`this.permissions.canReadEntity(context, entity.name)`, etc.) still typecheck unchanged, since `PermissionService`'s public method signatures didn't change, only its constructor did.

- [ ] **Step 8: Lint**

Run: `pnpm lint`
Expected: no new errors beyond whatever pre-existing lint state already exists in this repo (diff against a `git stash` baseline if unsure which errors are pre-existing).

- [ ] **Step 9: Leave the change in the working tree — do NOT commit**

Per this plan's Global Constraints, do not run `git commit`. Confirm via `git status` that `src/core/metadata/entity.ts`, `src/core/permission/permission-service.ts`, `src/core/container.ts`, and the new `src/core/permission/permission-service.test.ts` show as modified/untracked, and stop there.
