# Dynamic Role Assignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move role assignment out of the JWT and into the database — roles are resolved per-request from a `user_roles` table, can be granted/revoked at runtime without re-login, and are managed through a minimal admin API.

**Architecture:** New `user_roles` table + `RoleAssignmentService` (`src/core/auth/`). `auth-hook.ts`'s `onRequest` hook becomes async: it verifies the JWT (identity only, no `roles` claim), then queries `RoleAssignmentService` for the caller's current roles before building `RequestContext`. A new admin-gated route group manages assignments.

**Tech Stack:** Fastify, Zod, Drizzle ORM, PostgreSQL, vitest (live-DB integration tests, following the pattern in `src/core/crud/crud-service.test.ts`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-31-dynamic-role-assignment-design.md` — every task below implements a section of it.
- This is sub-project 1 of 4 for the "dynamic permission engine" initiative. Sub-projects 2-4 (policy storage, field/record enforcement, explainer+cache) are **out of scope** here.
- No policy/rule storage, no field/record-level enforcement, no caching of role lookups — explicitly deferred per the spec's non-goals.
- No `roles` catalog table, no `users` table — role names stay free-form strings; `userId` stays an opaque UUID.
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for the user to review at the end.
- `docker compose up -d postgres rabbitmq` must be running for any test/migration step.
- Run `pnpm typecheck` after any type-level change before moving to the next task. Pre-existing errors in `src/infra/messaging/rabbitmq.ts` are known and unrelated — do not try to fix them as part of this plan.

### Verified during planning (not just asserted)

No entity anywhere in `src/modules/` currently declares an `EntityPermissions` object (`permissions: {...}` on an `EntityDefinition`), which means `PermissionService.checkAction` always returns `{ allowed: true }` today regardless of the caller's roles, for every entity. Combined with the fact that `src/core/crud/crud-service.test.ts` builds its `RequestContext` by hand (never goes through the JWT/auth-hook path at all), this means **only two existing test files actually break** from removing `roles` from the JWT: `src/core/auth/jwt-verifier.test.ts` and `src/server/plugins/auth-hook.test.ts`. `src/server/app.test.ts` and `src/core/crud/crud-service.test.ts` are expected to keep passing unmodified — Task 4 includes a verification step that proves this rather than assuming it.

---

### Task 1: `user_roles` table + migration

**Files:**
- Modify: `src/infra/db/schema.ts` (add import, add table after `workflowEvents`)
- Create (generated): `src/infra/db/migrations/000X_*.sql`

**Interfaces:**
- Produces: `userRoles` Drizzle table — columns `id, tenantId, userId, role, createdAt, createdBy` — consumed by Task 2's `RoleAssignmentService`.

- [ ] **Step 1: Add the `uniqueIndex` import and the `userRoles` table**

In `src/infra/db/schema.ts`, change the import block (lines 2-12) from:

```ts
import {
  boolean,
  index,
  integer,
  jsonb,
  pgTable,
  text,
  timestamp,
  uuid,
  varchar,
} from "drizzle-orm/pg-core";
```

to:

```ts
import {
  boolean,
  index,
  integer,
  jsonb,
  pgTable,
  text,
  timestamp,
  uniqueIndex,
  uuid,
  varchar,
} from "drizzle-orm/pg-core";
```

Then insert this block after the `workflowEvents` table definition, before `export const recordRelations = relations(records, () => ({}));`:

```ts
export const userRoles = pgTable(
  "user_roles",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    userId: uuid("user_id").notNull(),
    role: varchar("role", { length: 80 }).notNull(),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    createdBy: uuid("created_by"),
  },
  (table) => ({
    tenantUserRoleUnique: uniqueIndex("user_roles_tenant_user_role_unique").on(
      table.tenantId,
      table.userId,
      table.role,
    ),
    tenantUserIdx: index("user_roles_tenant_user_idx").on(table.tenantId, table.userId),
  }),
);
```

- [ ] **Step 2: Generate the migration**

Run: `pnpm db:generate`
Expected: A new file under `src/infra/db/migrations/` containing `CREATE TABLE "user_roles"`, a unique index on `(tenant_id, user_id, role)`, and a plain index on `(tenant_id, user_id)`. Open it and confirm no unrelated diffs.

- [ ] **Step 3: Apply the migration**

Run: `pnpm db:migrate`
Expected: exits 0.

- [ ] **Step 4: Verify the table exists**

Run: `docker compose exec postgres psql -U metap -d metap -c '\d user_roles'`
Expected: 6 columns, a unique index, a plain index.

---

### Task 2: `RoleAssignmentService`

**Files:**
- Create: `src/core/auth/role-assignment-service.ts`
- Test: `src/core/auth/role-assignment-service.test.ts` (new)

**Interfaces:**
- Consumes: `userRoles` table (Task 1), `Database` (`src/infra/db/client.ts`).
- Produces (consumed by Task 3's auth hook and Task 6's admin routes):
  - `getRolesForUser(tenantId: string, userId: string): Promise<string[]>`
  - `assignRole(tenantId: string, userId: string, role: string, assignedBy: string | undefined): Promise<void>`
  - `revokeRole(tenantId: string, userId: string, role: string): Promise<void>`
  - `listUsers(tenantId: string): Promise<{ userId: string; roles: string[] }[]>`

- [ ] **Step 1: Write the failing tests**

Create `src/core/auth/role-assignment-service.test.ts`:

```ts
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { RoleAssignmentService } from "./role-assignment-service";

const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";

describe("RoleAssignmentService (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let service: RoleAssignmentService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000040";
  const otherTenantId = "00000000-0000-0000-0000-000000000099";
  const userId = "00000000-0000-0000-0000-000000000041";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new RoleAssignmentService(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping RoleAssignmentService live-DB tests: could not connect to ${databaseUrl}: ${
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
    await pgClient.query("DELETE FROM user_roles WHERE tenant_id IN ($1, $2)", [
      tenantId,
      otherTenantId,
    ]);
  }

  it("returns an empty array for a user with no assigned roles", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const roles = await service.getRolesForUser(tenantId, userId);
    expect(roles).toEqual([]);
  });

  it("assigns a role and returns it from getRolesForUser", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual(["admin"]);
    } finally {
      await cleanup();
    }
  });

  it("is idempotent: assigning the same role twice does not duplicate or throw", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.assignRole(tenantId, userId, "admin", undefined);
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual(["admin"]);
    } finally {
      await cleanup();
    }
  });

  it("revokes a role; revoking a role the user does not have is a no-op", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.revokeRole(tenantId, userId, "admin");
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual([]);

      await expect(service.revokeRole(tenantId, userId, "admin")).resolves.toBeUndefined();
    } finally {
      await cleanup();
    }
  });

  it("listUsers groups roles by user and does not leak other tenants", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const otherUserId = "00000000-0000-0000-0000-000000000042";

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.assignRole(tenantId, userId, "editor", undefined);
      await service.assignRole(otherTenantId, otherUserId, "admin", undefined);

      const users = await service.listUsers(tenantId);
      expect(users).toHaveLength(1);
      expect(users[0]?.userId).toBe(userId);
      expect(users[0]?.roles.sort()).toEqual(["admin", "editor"]);
    } finally {
      await cleanup();
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/auth/role-assignment-service.test.ts`
Expected: FAIL — the module `./role-assignment-service` does not exist.

- [ ] **Step 3: Implement `RoleAssignmentService`**

Create `src/core/auth/role-assignment-service.ts`:

```ts
import { and, eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { userRoles } from "../../infra/db/schema";

export class RoleAssignmentService {
  constructor(private readonly db: Database) {}

  async getRolesForUser(tenantId: string, userId: string): Promise<string[]> {
    const rows = await this.db.client
      .select({ role: userRoles.role })
      .from(userRoles)
      .where(and(eq(userRoles.tenantId, tenantId), eq(userRoles.userId, userId)));

    return rows.map((row) => row.role);
  }

  async assignRole(
    tenantId: string,
    userId: string,
    role: string,
    assignedBy: string | undefined,
  ): Promise<void> {
    await this.db.client
      .insert(userRoles)
      .values({ tenantId, userId, role, createdBy: assignedBy })
      .onConflictDoNothing({
        target: [userRoles.tenantId, userRoles.userId, userRoles.role],
      });
  }

  async revokeRole(tenantId: string, userId: string, role: string): Promise<void> {
    await this.db.client
      .delete(userRoles)
      .where(
        and(
          eq(userRoles.tenantId, tenantId),
          eq(userRoles.userId, userId),
          eq(userRoles.role, role),
        ),
      );
  }

  async listUsers(tenantId: string): Promise<{ userId: string; roles: string[] }[]> {
    const rows = await this.db.client
      .select({ userId: userRoles.userId, role: userRoles.role })
      .from(userRoles)
      .where(eq(userRoles.tenantId, tenantId));

    const grouped = new Map<string, string[]>();
    for (const row of rows) {
      const roles = grouped.get(row.userId) ?? [];
      roles.push(row.role);
      grouped.set(row.userId, roles);
    }

    return [...grouped.entries()].map(([userId, roles]) => ({ userId, roles }));
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/auth/role-assignment-service.test.ts`
Expected: PASS (5 tests).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors (pre-existing `rabbitmq.ts` errors only).

---

### Task 3: Rewire the auth flow to resolve roles from the database

**Files:**
- Modify: `src/core/auth/jwt-verifier.ts` (drop `roles` from `ClaimsSchema`)
- Modify: `src/core/auth/request-context.ts` (full file)
- Modify: `src/server/plugins/auth-hook.ts` (full file)
- Modify: `src/core/container.ts` (full file)
- Modify: `src/server/app.ts` (one call site)

**Interfaces:**
- Consumes: `RoleAssignmentService` (Task 2).
- Produces: `container.roleAssignments: RoleAssignmentService`; `registerAuthHook(instance, verifier, roleAssignments)` — third parameter typed `Pick<RoleAssignmentService, "getRolesForUser">` so tests can pass a plain fake instead of a real DB-backed instance.

- [ ] **Step 1: Drop `roles` from `ClaimsSchema`**

In `src/core/auth/jwt-verifier.ts`, change:

```ts
const ClaimsSchema = z.object({
  sub: z.string().min(1),
  tenantId: z.string().min(1),
  roles: z.array(z.string()).default([]),
  functionId: z.string().optional(),
  exp: z.number(),
});
```

to:

```ts
const ClaimsSchema = z.object({
  sub: z.string().min(1),
  tenantId: z.string().min(1),
  functionId: z.string().optional(),
  exp: z.number(),
});
```

- [ ] **Step 2: `buildRequestContext` takes roles as a parameter**

Replace the full contents of `src/core/auth/request-context.ts` with:

```ts
import type { RequestContext } from "../permission/permission-service";
import type { Claims } from "./jwt-verifier";

export function buildRequestContext(claims: Claims, roles: readonly string[]): RequestContext {
  const context: RequestContext = {
    tenantId: claims.tenantId,
    userId: claims.sub,
    roles,
  };

  if (claims.functionId !== undefined) {
    context.functionId = claims.functionId;
  }

  return context;
}
```

- [ ] **Step 3: Make the auth hook async and resolve roles from `RoleAssignmentService`**

Replace the full contents of `src/server/plugins/auth-hook.ts` with:

```ts
import type { FastifyInstance } from "fastify";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import { AuthError } from "../../core/auth/errors";
import type { RequestContext } from "../../core/permission/permission-service";
import { buildRequestContext } from "../../core/auth/request-context";
import type { RoleAssignmentService } from "../../core/auth/role-assignment-service";

declare module "fastify" {
  interface FastifyRequest {
    context: RequestContext;
  }
}

const BEARER_PREFIX = "Bearer ";

export function registerAuthHook(
  instance: FastifyInstance,
  verifier: JwtVerifier,
  roleAssignments: Pick<RoleAssignmentService, "getRolesForUser">,
) {
  instance.decorateRequest("context", null, []);

  instance.addHook("onRequest", async (request) => {
    const header = request.headers.authorization;

    if (!header || !header.startsWith(BEARER_PREFIX)) {
      throw new AuthError("Missing or invalid authorization header.");
    }

    const token = header.slice(BEARER_PREFIX.length);
    const claims = verifier.verify(token);
    const roles = await roleAssignments.getRolesForUser(claims.tenantId, claims.sub);
    request.context = buildRequestContext(claims, roles);
  });
}
```

Note: the previous try/catch that converted unexpected errors into a generic `Error` is gone — it's no longer needed. `verifier.verify` only ever throws `AuthError` (see `jwt-verifier.ts`), and any error thrown or rejected inside a Fastify async hook already reaches `registerErrorHandler`'s global handler, which falls through to a 500 for anything that isn't `AuthError`/`ZodError`/a recognized HTTP error — the exact same behavior the old catch-and-rewrap block was manually producing.

- [ ] **Step 4: Wire `RoleAssignmentService` into the container**

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

  const permissions = new PermissionService(metadata);
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

- [ ] **Step 5: Pass `roleAssignments` to `registerAuthHook`**

In `src/server/app.ts`, change:

```ts
    registerAuthHook(protectedApp, container.auth);
```

to:

```ts
    registerAuthHook(protectedApp, container.auth, container.roleAssignments);
```

- [ ] **Step 6: Typecheck**

Run: `pnpm typecheck`
Expected: errors in `src/core/auth/jwt-verifier.test.ts` and `src/server/plugins/auth-hook.test.ts` (they call `registerAuthHook`/build claims with the old signature) — expected at this point, fixed in Task 4. No errors anywhere else.

---

### Task 4: Fix the two broken tests; verify the rest are unaffected

**Files:**
- Modify: `src/core/auth/jwt-verifier.test.ts` (full file)
- Modify: `src/server/plugins/auth-hook.test.ts` (full file)

**Interfaces:**
- Consumes: `RoleAssignmentService` (Task 2, type only — via `Pick<..., "getRolesForUser">`), `registerAuthHook`'s new 3-arg signature (Task 3).

- [ ] **Step 1: Fix `jwt-verifier.test.ts`**

Replace the full contents of `src/core/auth/jwt-verifier.test.ts` with:

```ts
import { generateKeyPairSync } from "node:crypto";
import jwt from "jsonwebtoken";
import { describe, expect, it } from "vitest";
import { AuthError } from "./errors";
import { verifyToken } from "./jwt-verifier";

function makeKeyPair() {
  return generateKeyPairSync("rsa", {
    modulusLength: 2048,
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });
}

describe("verifyToken", () => {
  it("returns claims for a validly signed token", () => {
    const { publicKey, privateKey } = makeKeyPair();
    const token = jwt.sign({ tenantId: "tenant-1" }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const claims = verifyToken(token, publicKey);
    expect(claims).toMatchObject({ sub: "user-1", tenantId: "tenant-1" });
    expect(typeof claims.exp).toBe("number");
  });

  it("rejects a token signed with a different key", () => {
    const { privateKey } = makeKeyPair();
    const { publicKey: otherPublicKey } = makeKeyPair();
    const token = jwt.sign({ tenantId: "tenant-1" }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
    });

    expect(() => verifyToken(token, otherPublicKey)).toThrow(AuthError);
  });

  it("rejects a token missing the tenantId claim", () => {
    const { publicKey, privateKey } = makeKeyPair();
    const token = jwt.sign({}, privateKey, { algorithm: "RS256", subject: "user-1" });

    expect(() => verifyToken(token, publicKey)).toThrow(AuthError);
  });
});
```

(Only change from the original: the signed payload no longer includes `roles`, and the assertion no longer checks for a `roles` key — `Claims` no longer has one.)

- [ ] **Step 2: Fix `auth-hook.test.ts`, and add a test proving JWT roles are ignored**

Replace the full contents of `src/server/plugins/auth-hook.test.ts` with:

```ts
import { generateKeyPairSync } from "node:crypto";
import Fastify from "fastify";
import jwt from "jsonwebtoken";
import { describe, expect, it } from "vitest";
import { verifyToken } from "../../core/auth/jwt-verifier";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import type { RoleAssignmentService } from "../../core/auth/role-assignment-service";
import { registerErrorHandler } from "../error-handler";
import { registerAuthHook } from "./auth-hook";
import { registerRequestContextHooks } from "./request-id";

function buildTestApp(
  verifier: JwtVerifier,
  roleAssignments: Pick<RoleAssignmentService, "getRolesForUser">,
) {
  const app = Fastify();

  registerRequestContextHooks(app);
  registerErrorHandler(app);
  registerAuthHook(app, verifier, roleAssignments);

  app.get("/protected", async (request) => ({ context: request.context }));

  return app;
}

describe("auth hook", () => {
  const { publicKey, privateKey } = generateKeyPairSync("rsa", {
    modulusLength: 2048,
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });
  const verifier: JwtVerifier = {
    verify: (token) => verifyToken(token, publicKey),
  };

  it("rejects a request with no authorization header", async () => {
    const roleAssignments = { getRolesForUser: async () => [] };
    const app = buildTestApp(verifier, roleAssignments);
    const response = await app.inject({ method: "GET", url: "/protected" });

    expect(response.statusCode).toBe(401);
    expect(response.json()).toMatchObject({ error: { code: "unauthorized" } });
  });

  it("attaches request context with roles resolved from RoleAssignmentService", async () => {
    const roleAssignments = { getRolesForUser: async () => ["admin"] };
    const app = buildTestApp(verifier, roleAssignments);
    const token = jwt.sign({ tenantId: "tenant-1" }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const response = await app.inject({
      method: "GET",
      url: "/protected",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({
      context: { tenantId: "tenant-1", userId: "user-1", roles: ["admin"] },
    });
  });

  it("ignores a roles claim embedded in the JWT — roles always come from RoleAssignmentService", async () => {
    const roleAssignments = { getRolesForUser: async () => ["admin"] };
    const app = buildTestApp(verifier, roleAssignments);
    const token = jwt.sign({ tenantId: "tenant-1", roles: ["superadmin"] }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const response = await app.inject({
      method: "GET",
      url: "/protected",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({
      context: { tenantId: "tenant-1", userId: "user-1", roles: ["admin"] },
    });
  });
});
```

- [ ] **Step 3: Run the full test suite and confirm the "verified during planning" claim**

Run: `pnpm test`
Expected: all tests pass, including `src/server/app.test.ts` and `src/core/crud/crud-service.test.ts` **unmodified** — confirming the Global Constraints analysis that these two files never depended on JWT-encoded roles. If either fails unexpectedly, stop and investigate before continuing — that would mean the "verified during planning" analysis missed something real.

- [ ] **Step 4: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

---

### Task 5: Update dev scripts

**Files:**
- Modify: `scripts/mint-dev-token.mjs` (full file)
- Create: `scripts/seed-admin.mjs`
- Modify: `package.json` (add `seed:admin` script)

**Interfaces:**
- Consumes: `user_roles` table (Task 1).
- Produces: nothing consumed by later tasks — these are standalone dev-workflow scripts.

- [ ] **Step 1: Drop the `roles` argument from `mint-dev-token.mjs`**

Replace the full contents of `scripts/mint-dev-token.mjs` with:

```js
import jwt from "jsonwebtoken";
import { readFileSync } from "node:fs";

const tenantId = process.argv[2] ?? "00000000-0000-0000-0000-000000000001";
const userId = process.argv[3] ?? "00000000-0000-0000-0000-000000000002";

const privateKey = readFileSync("keys/dev-jwt-private.pem", "utf8");

const token = jwt.sign({ tenantId }, privateKey, {
  algorithm: "RS256",
  subject: userId,
  expiresIn: "1h",
});

console.log(token);
```

- [ ] **Step 2: Add `scripts/seed-admin.mjs`**

Create `scripts/seed-admin.mjs`:

```js
import "dotenv/config";
import { Client } from "pg";

const tenantId = process.argv[2];
const userId = process.argv[3];

if (!tenantId || !userId) {
  console.error("Usage: pnpm seed:admin <tenantId> <userId>");
  process.exit(1);
}

const client = new Client({ connectionString: process.env.DATABASE_URL });
await client.connect();

try {
  await client.query(
    `INSERT INTO user_roles (tenant_id, user_id, role)
     VALUES ($1, $2, 'admin')
     ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
    [tenantId, userId],
  );
  console.log(`Granted 'admin' role to user ${userId} in tenant ${tenantId}.`);
} finally {
  await client.end();
}
```

- [ ] **Step 3: Add the `seed:admin` package.json script**

In `package.json`, change:

```json
    "auth:dev-keys": "node scripts/generate-dev-jwt-keypair.mjs",
    "mint-token": "node scripts/mint-dev-token.mjs"
```

to:

```json
    "auth:dev-keys": "node scripts/generate-dev-jwt-keypair.mjs",
    "mint-token": "node scripts/mint-dev-token.mjs",
    "seed:admin": "node scripts/seed-admin.mjs"
```

- [ ] **Step 4: Manually verify the scripts work**

Run:

```bash
pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
```

Expected: prints `Granted 'admin' role to user 00000000-0000-0000-0000-000000000002 in tenant 00000000-0000-0000-0000-000000000001.` Run it a second time with the same arguments — expected: same output, no error (idempotent).

Then:

```bash
docker compose exec postgres psql -U metap -d metap -c "SELECT tenant_id, user_id, role FROM user_roles;"
```

Expected: one row with the tenant/user/role above. Clean it up afterward: `docker compose exec postgres psql -U metap -d metap -c "DELETE FROM user_roles WHERE user_id = '00000000-0000-0000-0000-000000000002';"` (Task 6's manual verification seeds its own admin fresh, so this shouldn't be left behind.)

---

### Task 6: Admin API — assign/revoke/list role assignments

**Files:**
- Create: `src/server/routes/admin.ts`
- Modify: `src/server/app.ts` (register the route group)
- Test: `src/server/routes/admin.test.ts` (new)

**Interfaces:**
- Consumes: `container.roleAssignments` (Task 3/2), `sendServiceError` (`src/server/error-handler.ts`).
- Produces: nothing consumed by later tasks (last task in this plan).

- [ ] **Step 1: Write the failing tests**

Create `src/server/routes/admin.test.ts`:

```ts
import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import jwt from "jsonwebtoken";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildApp } from "../app";
import type { AppConfig } from "../config";

describe("admin routes (live DB)", () => {
  const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";
  const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

  const tenantId = "00000000-0000-0000-0000-000000000050";
  const adminUserId = "00000000-0000-0000-0000-000000000051";
  const targetUserId = "00000000-0000-0000-0000-000000000052";

  let app: FastifyInstance;
  let tmpDir: string;
  let privateKey: string;
  let pgClient: Client;
  let dbAvailable = true;
  let adminToken: string;
  let nonAdminToken: string;

  beforeAll(async () => {
    const keyPair = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });
    privateKey = keyPair.privateKey;

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-admin-routes-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, keyPair.publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    app = await buildApp(config);

    adminToken = jwt.sign({ tenantId }, privateKey, {
      algorithm: "RS256",
      subject: adminUserId,
      expiresIn: "1h",
    });
    nonAdminToken = jwt.sign({ tenantId }, privateKey, {
      algorithm: "RS256",
      subject: targetUserId,
      expiresIn: "1h",
    });

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
      await pgClient.query(
        `INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')
         ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
        [tenantId, adminUserId],
      );
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping admin routes live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("rejects a non-admin caller with 403", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/users",
      headers: { authorization: `Bearer ${nonAdminToken}` },
    });

    expect(response.statusCode).toBe(403);
    expect(response.json()).toMatchObject({ error: { code: "forbidden" } });
  });

  it("assigns a role, lists it, then revokes it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const assignResponse = await app.inject({
      method: "POST",
      url: `/admin/users/${targetUserId}/roles`,
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { role: "editor" },
    });

    expect(assignResponse.statusCode).toBe(201);
    expect(assignResponse.json()).toEqual({ data: ["editor"] });

    const listResponse = await app.inject({
      method: "GET",
      url: `/admin/users/${targetUserId}/roles`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(listResponse.statusCode).toBe(200);
    expect(listResponse.json()).toEqual({ data: ["editor"] });

    const revokeResponse = await app.inject({
      method: "DELETE",
      url: `/admin/users/${targetUserId}/roles/editor`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(revokeResponse.statusCode).toBe(200);
    expect(revokeResponse.json()).toEqual({ data: [] });
  });

  it("assigning the same role twice is idempotent", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await app.inject({
        method: "POST",
        url: `/admin/users/${targetUserId}/roles`,
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { role: "editor" },
      });
      const second = await app.inject({
        method: "POST",
        url: `/admin/users/${targetUserId}/roles`,
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { role: "editor" },
      });

      expect(second.statusCode).toBe(201);
      expect(second.json()).toEqual({ data: ["editor"] });
    } finally {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2", [
        tenantId,
        targetUserId,
      ]);
    }
  });

  it("lists users with assigned roles in the tenant", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/users",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(response.statusCode).toBe(200);
    const body = response.json<{ data: { userId: string; roles: string[] }[] }>();
    const adminEntry = body.data.find((u) => u.userId === adminUserId);
    expect(adminEntry?.roles).toEqual(["admin"]);
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: FAIL — `/admin/users` routes don't exist yet (404s, and the module import of `registerAdminRoutes` doesn't exist so the route registration this test relies on via `buildApp` isn't there — the assertions on status codes will fail since nothing is wired).

- [ ] **Step 3: Implement `registerAdminRoutes`**

Create `src/server/routes/admin.ts`:

```ts
import { z } from "zod";
import type { FastifyInstance, FastifyRequest } from "fastify";
import { zodToJsonSchema } from "zod-to-json-schema";
import type { AppContainer } from "../../core/container";
import { sendServiceError } from "../error-handler";

const UserIdParamsSchema = z.object({ userId: z.string().uuid() });
const RoleParamsSchema = z.object({ userId: z.string().uuid(), role: z.string().min(1) });
const AssignRoleBodySchema = z.object({ role: z.string().min(1) });

function isAdmin(request: FastifyRequest) {
  return request.context.roles?.includes("admin") ?? false;
}

export function registerAdminRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/admin/users", async (request, reply) => {
    if (!isAdmin(request)) {
      return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
    }

    const users = await container.roleAssignments.listUsers(request.context.tenantId);
    return { data: users };
  });

  app.get<{ Params: { userId: string } }>(
    "/admin/users/:userId/roles",
    { schema: { params: zodToJsonSchema(UserIdParamsSchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = UserIdParamsSchema.parse(request.params);
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return { data: roles };
    },
  );

  app.post<{ Params: { userId: string }; Body: z.infer<typeof AssignRoleBodySchema> }>(
    "/admin/users/:userId/roles",
    {
      schema: {
        params: zodToJsonSchema(UserIdParamsSchema),
        body: zodToJsonSchema(AssignRoleBodySchema),
      },
    },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = UserIdParamsSchema.parse(request.params);
      const body = AssignRoleBodySchema.parse(request.body);
      await container.roleAssignments.assignRole(
        request.context.tenantId,
        params.userId,
        body.role,
        request.context.userId,
      );
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return reply.code(201).send({ data: roles });
    },
  );

  app.delete<{ Params: { userId: string; role: string } }>(
    "/admin/users/:userId/roles/:role",
    { schema: { params: zodToJsonSchema(RoleParamsSchema) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = RoleParamsSchema.parse(request.params);
      await container.roleAssignments.revokeRole(
        request.context.tenantId,
        params.userId,
        params.role,
      );
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return { data: roles };
    },
  );
}
```

- [ ] **Step 4: Register the route group in `app.ts`**

In `src/server/app.ts`, add the import:

```ts
import { registerRecordRoutes } from "./routes/records";
```

becomes:

```ts
import { registerAdminRoutes } from "./routes/admin";
import { registerRecordRoutes } from "./routes/records";
```

And change:

```ts
  await app.register(async (protectedApp) => {
    registerAuthHook(protectedApp, container.auth, container.roleAssignments);
    registerMetadataRoutes(protectedApp, container);
    registerRecordRoutes(protectedApp, container);
  });
```

to:

```ts
  await app.register(async (protectedApp) => {
    registerAuthHook(protectedApp, container.auth, container.roleAssignments);
    registerMetadataRoutes(protectedApp, container);
    registerRecordRoutes(protectedApp, container);
    registerAdminRoutes(protectedApp, container);
  });
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `pnpm vitest run src/server/routes/admin.test.ts`
Expected: PASS (4 tests).

- [ ] **Step 6: Full test suite, typecheck, lint**

Run: `pnpm test && pnpm typecheck`
Expected: all tests pass (this plan's new suites plus every pre-existing one); no new typecheck errors.

Run: `pnpm lint`
Expected: no new lint errors in any file this plan touched (pre-existing errors elsewhere, e.g. `rabbitmq.ts`, are out of scope — compare against the baseline the same way earlier plans in this repo have, e.g. `git stash` and re-run to diff).

- [ ] **Step 7: Manual verification against the dev server**

Run: `pnpm dev` (background), then:

```bash
pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
TOKEN=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002)

# list users — should show the seeded admin
curl -s http://localhost:3000/admin/users -H "Authorization: Bearer $TOKEN"

# assign a role to a new user
curl -s -X POST http://localhost:3000/admin/users/00000000-0000-0000-0000-000000000099/roles \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"role":"editor"}'

# a token for that new (non-admin) user should get 403 on /admin/users
TOKEN2=$(pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000099)
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:3000/admin/users -H "Authorization: Bearer $TOKEN2"
# expect 403

# revoke it
curl -s -X DELETE http://localhost:3000/admin/users/00000000-0000-0000-0000-000000000099/roles/editor \
  -H "Authorization: Bearer $TOKEN"
```

Confirm each response matches expectations, then clean up the seeded rows:

```bash
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM user_roles WHERE tenant_id = '00000000-0000-0000-0000-000000000001';"
```

Stop the dev server afterward.

---

## Plan Self-Review Notes

- **Spec coverage:** §1 (auth flow) → Task 3. §2 (`user_roles` schema) → Task 1. §3 (`RoleAssignmentService`) → Task 2. §4 (admin API) → Task 6. §5 (bootstrap) → Task 5. "Consequences for existing tests" → Task 4, with the scope corrected/narrowed from "four files" to the two that actually break, per the empirical grep done during planning (documented in Global Constraints).
- **No placeholders:** every step has literal code.
- **Type consistency checked:** `RoleAssignmentService`'s four method signatures (Task 2) match their call sites in `auth-hook.ts` (Task 3, via `Pick<..., "getRolesForUser">`) and `admin.ts` (Task 6) exactly. `buildRequestContext(claims, roles)`'s new signature (Task 3) matches its only call site in the rewritten `auth-hook.ts`. `container.roleAssignments` (Task 3) matches its consumption in `app.ts` (Task 3) and `admin.ts` (Task 6).
