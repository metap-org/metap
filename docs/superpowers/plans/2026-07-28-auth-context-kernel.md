# Auth + RequestContext + Structured Errors Kernel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `CrudService`'s hardcoded `defaultContext()` with real, server-verified JWT auth, deterministic `RequestContext`, structured error responses, and request/trace id propagation.

**Architecture:** A root-level Fastify hook assigns `requestId`/`traceId` and binds them to a per-request child logger, before anything else runs. A root-level `setErrorHandler` formats every thrown error (auth failures, Zod validation errors, `CrudService`'s existing `ServiceResult` failures, unexpected exceptions) into one JSON shape. `/health` is registered directly on the root app and is never touched by auth. `/metadata/*` and `/api/:entity` are registered inside a nested Fastify plugin scope that has a JWT auth hook applied — any route added to that scope in the future is protected automatically, nothing opts in per-route.

**Tech Stack:** Fastify 5, `jsonwebtoken` (new dependency, synchronous RS256 verify — no callback form, which is what makes a known class of context-built-before-verify race structurally impossible here), Zod, Vitest.

## Global Constraints

- Node >=24.15.0, pnpm package manager, ESM throughout (`"type": "module"`) — no `require()` in source files (test/eval scripts using `node -e` are fine, see Task 6).
- TypeScript strict mode with `exactOptionalPropertyTypes: true` — never assign `undefined` directly to an optional property; omit the key instead.
- JWT verification uses `jsonwebtoken`'s **synchronous** `jwt.verify(token, key, opts)` form only (no callback argument) — this is a hard requirement from the spec, not a style preference.
- Algorithm is fixed to `RS256`. The public key path (`AUTH_JWT_PUBLIC_KEY_PATH`) is **required** in every environment; the app must fail to start if it can't be read.
- A single `AuthError` (401, code `unauthorized`) covers all auth failures for now — no finer-grained auth error codes.
- `requestId` (fresh every request, Fastify's built-in `request.id`) and `traceId` (from an incoming `x-trace-id` header, else generated) are two distinct fields, both always present in the structured error body and as response headers.
- `/health` is the only public route. Everything else requires auth.
- Testing is scoped to important cases only, per project preference — no exhaustive input matrices. Each task's test list below is deliberately short; don't add more.

---

### Task 1: Auth core — errors, JWT verification, RequestContext builder

**Files:**
- Create: `src/core/auth/errors.ts`
- Create: `src/core/auth/jwt-verifier.ts`
- Create: `src/core/auth/jwt-verifier.test.ts`
- Create: `src/core/auth/request-context.ts`
- Modify: `package.json` (add `jsonwebtoken` + `@types/jsonwebtoken`)

**Interfaces:**
- Produces: `class AuthError extends Error` with `statusCode: 401` and `code: "unauthorized"`. `type Claims = { sub: string; tenantId: string; roles: string[]; functionId?: string }`. `function verifyToken(token: string, publicKey: string): Claims` (throws `AuthError`). `type JwtVerifier = { verify(token: string): Claims }`. `function createJwtVerifier(publicKeyPath: string): JwtVerifier`. `function buildRequestContext(claims: Claims): RequestContext`.
- Consumes: `RequestContext` type from `src/core/permission/permission-service.ts` (already exists, unchanged).

- [ ] **Step 1: Add the dependency**

Run: `pnpm add jsonwebtoken && pnpm add -D @types/jsonwebtoken`

- [ ] **Step 2: Write `src/core/auth/errors.ts`**

```ts
export class AuthError extends Error {
  readonly statusCode = 401;
  readonly code = "unauthorized";

  constructor(message: string) {
    super(message);
    this.name = "AuthError";
  }
}
```

- [ ] **Step 3: Write `src/core/auth/jwt-verifier.ts`**

```ts
import fs from "node:fs";
import jwt from "jsonwebtoken";
import { z } from "zod";
import { AuthError } from "./errors";

const ClaimsSchema = z.object({
  sub: z.string().min(1),
  tenantId: z.string().min(1),
  roles: z.array(z.string()).default([]),
  functionId: z.string().optional(),
});

export type Claims = z.infer<typeof ClaimsSchema>;

export function verifyToken(token: string, publicKey: string): Claims {
  let payload: unknown;

  try {
    payload = jwt.verify(token, publicKey, { algorithms: ["RS256"] });
  } catch {
    throw new AuthError("Invalid or expired token.");
  }

  const parsed = ClaimsSchema.safeParse(payload);

  if (!parsed.success) {
    throw new AuthError("Token is missing required claims.");
  }

  return parsed.data;
}

export type JwtVerifier = {
  verify(token: string): Claims;
};

export function createJwtVerifier(publicKeyPath: string): JwtVerifier {
  const publicKey = fs.readFileSync(publicKeyPath, "utf8");

  return {
    verify(token: string) {
      return verifyToken(token, publicKey);
    },
  };
}
```

- [ ] **Step 4: Write the failing tests, `src/core/auth/jwt-verifier.test.ts`**

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
    const token = jwt.sign({ tenantId: "tenant-1", roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
    });

    expect(verifyToken(token, publicKey)).toEqual({
      sub: "user-1",
      tenantId: "tenant-1",
      roles: ["admin"],
    });
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

- [ ] **Step 5: Run the tests to verify they fail (or pass, since the implementation already exists above)**

Run: `pnpm vitest run src/core/auth/jwt-verifier.test.ts`
Expected: 3 passed (the implementation in Step 3 was written alongside the test, so this confirms correctness rather than a red/green cycle — that's fine for this task).

- [ ] **Step 6: Write `src/core/auth/request-context.ts`**

`exactOptionalPropertyTypes` means `functionId` must be omitted from the object entirely when absent, not assigned `undefined`:

```ts
import type { RequestContext } from "../permission/permission-service";
import type { Claims } from "./jwt-verifier";

export function buildRequestContext(claims: Claims): RequestContext {
  const context: RequestContext = {
    tenantId: claims.tenantId,
    userId: claims.sub,
    roles: claims.roles,
  };

  if (claims.functionId !== undefined) {
    context.functionId = claims.functionId;
  }

  return context;
}
```

- [ ] **Step 7: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 8: Commit**

```bash
git add package.json pnpm-lock.yaml src/core/auth/
git commit -m "Add JWT verification, AuthError, and RequestContext builder"
```

---

### Task 2: Request id / trace id root hook

**Files:**
- Create: `src/server/plugins/request-id.ts`

**Interfaces:**
- Produces: `function registerRequestContextHooks(app: FastifyInstance): void`. Module augmentation adding `traceId: string` to `FastifyRequest`.
- Consumes: nothing from Task 1.

- [ ] **Step 1: Write `src/server/plugins/request-id.ts`**

```ts
import { randomUUID } from "node:crypto";
import type { FastifyInstance } from "fastify";

declare module "fastify" {
  interface FastifyRequest {
    traceId: string;
  }
}

export function registerRequestContextHooks(app: FastifyInstance) {
  app.addHook("onRequest", (request, reply, done) => {
    const incomingTraceId = request.headers["x-trace-id"];
    const traceId =
      typeof incomingTraceId === "string" && incomingTraceId.length > 0
        ? incomingTraceId
        : randomUUID();

    request.traceId = traceId;
    request.log = request.log.child({ traceId });

    reply.header("x-request-id", request.id);
    reply.header("x-trace-id", traceId);

    done();
  });
}
```

No dedicated automated test for this step (per the project's minimal-testing preference — this is a simple, low-risk hook). It's exercised indirectly by Task 4's auth-hook test (which reads `request.traceId` through the error handler) and verified manually via `curl -i` in Task 6.

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add src/server/plugins/request-id.ts
git commit -m "Add request id / trace id hook"
```

---

### Task 3: Structured error handler

**Files:**
- Create: `src/server/error-handler.ts`

**Interfaces:**
- Consumes: `AuthError` from `src/core/auth/errors.ts` (Task 1). `ServiceResult` from `src/core/crud/result.ts` (existing). `request.traceId` from Task 2.
- Produces: `function registerErrorHandler(app: FastifyInstance): void`. `function sendServiceError(request: FastifyRequest, reply: FastifyReply, result: Extract<ServiceResult<unknown>, { ok: false }>): FastifyReply`.

- [ ] **Step 1: Write `src/server/error-handler.ts`**

```ts
import { ZodError } from "zod";
import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { AuthError } from "../core/auth/errors";
import type { ServiceResult } from "../core/crud/result";

type ErrorBody = {
  error: {
    code: string;
    message: string;
    requestId: string;
    traceId: string;
  };
};

function errorBody(request: FastifyRequest, code: string, message: string): ErrorBody {
  return {
    error: {
      code,
      message,
      requestId: request.id,
      traceId: request.traceId,
    },
  };
}

export function registerErrorHandler(app: FastifyInstance) {
  app.setErrorHandler((error, request, reply) => {
    if (error instanceof AuthError) {
      return reply.code(error.statusCode).send(errorBody(request, error.code, error.message));
    }

    if (error instanceof ZodError) {
      return reply
        .code(400)
        .send(errorBody(request, "validation_failed", "Request validation failed."));
    }

    request.log.error(error);
    return reply.code(500).send(errorBody(request, "internal_error", "Internal server error."));
  });
}

const SERVICE_ERROR_MESSAGES: Record<string, string> = {
  entity_not_found: "Entity not found.",
  forbidden: "You do not have permission to perform this action.",
  validation_failed: "Request validation failed.",
  insert_failed: "Failed to create the record.",
};

export function sendServiceError(
  request: FastifyRequest,
  reply: FastifyReply,
  result: Extract<ServiceResult<unknown>, { ok: false }>,
) {
  const message = SERVICE_ERROR_MESSAGES[result.error] ?? result.error;
  return reply.code(result.status).send(errorBody(request, result.error, message));
}
```

No dedicated test in this task — the `AuthError` path is exercised by Task 4's auth-hook test, and this is otherwise a straightforward mapping table.

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add src/server/error-handler.ts
git commit -m "Add structured error handler and sendServiceError helper"
```

---

### Task 4: Auth hook plugin

**Files:**
- Create: `src/server/plugins/auth-hook.ts`
- Create: `src/server/plugins/auth-hook.test.ts`

**Interfaces:**
- Consumes: `JwtVerifier`, `verifyToken` from `src/core/auth/jwt-verifier.ts`. `AuthError` from `src/core/auth/errors.ts`. `buildRequestContext` from `src/core/auth/request-context.ts`. `registerErrorHandler` from `src/server/error-handler.ts`. `registerRequestContextHooks` from `src/server/plugins/request-id.ts`.
- Produces: `function registerAuthHook(instance: FastifyInstance, verifier: JwtVerifier): void`. Module augmentation adding `context: RequestContext` to `FastifyRequest`.

- [ ] **Step 1: Write `src/server/plugins/auth-hook.ts`**

```ts
import type { FastifyInstance } from "fastify";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import { AuthError } from "../../core/auth/errors";
import type { RequestContext } from "../../core/permission/permission-service";
import { buildRequestContext } from "../../core/auth/request-context";

declare module "fastify" {
  interface FastifyRequest {
    context: RequestContext;
  }
}

const BEARER_PREFIX = "Bearer ";

export function registerAuthHook(instance: FastifyInstance, verifier: JwtVerifier) {
  instance.decorateRequest("context", null);

  instance.addHook("onRequest", (request, _reply, done) => {
    const header = request.headers.authorization;

    if (!header || !header.startsWith(BEARER_PREFIX)) {
      done(new AuthError("Missing or invalid authorization header."));
      return;
    }

    const token = header.slice(BEARER_PREFIX.length);

    try {
      const claims = verifier.verify(token);
      request.context = buildRequestContext(claims);
      done();
    } catch (error) {
      done(error instanceof Error ? error : new AuthError("Invalid token."));
    }
  });
}
```

- [ ] **Step 2: Write the failing tests, `src/server/plugins/auth-hook.test.ts`**

```ts
import { generateKeyPairSync } from "node:crypto";
import Fastify from "fastify";
import jwt from "jsonwebtoken";
import { describe, expect, it } from "vitest";
import { verifyToken } from "../../core/auth/jwt-verifier";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import { registerErrorHandler } from "../error-handler";
import { registerAuthHook } from "./auth-hook";
import { registerRequestContextHooks } from "./request-id";

function buildTestApp(verifier: JwtVerifier) {
  const app = Fastify();

  registerRequestContextHooks(app);
  registerErrorHandler(app);
  registerAuthHook(app, verifier);

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
    const app = buildTestApp(verifier);
    const response = await app.inject({ method: "GET", url: "/protected" });

    expect(response.statusCode).toBe(401);
    expect(response.json()).toMatchObject({ error: { code: "unauthorized" } });
  });

  it("attaches request context for a validly signed token", async () => {
    const app = buildTestApp(verifier);
    const token = jwt.sign({ tenantId: "tenant-1", roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
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

- [ ] **Step 3: Run the tests**

Run: `pnpm vitest run src/server/plugins/auth-hook.test.ts`
Expected: 2 passed.

- [ ] **Step 4: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add src/server/plugins/auth-hook.ts src/server/plugins/auth-hook.test.ts
git commit -m "Add JWT auth hook plugin"
```

---

### Task 5: Wire into the real app — config, container, app.ts, dev keys

**Files:**
- Modify: `src/server/config.ts`
- Modify: `src/core/container.ts`
- Modify: `src/server/app.ts`
- Create: `scripts/generate-dev-jwt-keypair.mjs`
- Modify: `package.json` (add `auth:dev-keys` script)
- Modify: `.gitignore`
- Modify: `.env.example`

**Interfaces:**
- Consumes: `createJwtVerifier` (Task 1), `registerRequestContextHooks` (Task 2), `registerErrorHandler` (Task 3), `registerAuthHook` (Task 4).
- Produces: `container.auth: JwtVerifier`, available to `app.ts` and (later) tests.

- [ ] **Step 1: Require the public key path in config — modify `src/server/config.ts`**

Change:
```ts
  authJwtPublicKeyPath: z.string().optional(),
```
to:
```ts
  authJwtPublicKeyPath: z.string().min(1, "AUTH_JWT_PUBLIC_KEY_PATH is required"),
```
And change:
```ts
    authJwtPublicKeyPath: process.env.AUTH_JWT_PUBLIC_KEY_PATH || undefined,
```
to:
```ts
    authJwtPublicKeyPath: process.env.AUTH_JWT_PUBLIC_KEY_PATH,
```

- [ ] **Step 2: Wire the verifier into the container — modify `src/core/container.ts`**

Add the import:
```ts
import { createJwtVerifier } from "./auth/jwt-verifier";
```
Add, after `const db = createDatabase(config.databaseUrl);`:
```ts
  const auth = createJwtVerifier(config.authJwtPublicKeyPath);
```
Add `auth,` to the returned object (alongside `db, rabbit, metadata, ...`).

- [ ] **Step 3: Restructure `src/server/app.ts` to add hooks and the protected scope**

```ts
import cors from "@fastify/cors";
import helmet from "@fastify/helmet";
import rateLimit from "@fastify/rate-limit";
import Fastify from "fastify";
import type { AppConfig } from "./config";
import { createContainer } from "../core/container";
import { registerErrorHandler } from "./error-handler";
import { registerAuthHook } from "./plugins/auth-hook";
import { registerRequestContextHooks } from "./plugins/request-id";
import { registerHealthRoutes } from "./routes/health";
import { registerMetadataRoutes } from "./routes/metadata";
import { registerRecordRoutes } from "./routes/records";

export async function buildApp(config: AppConfig) {
  const app = Fastify({
    logger: {
      level: config.nodeEnv === "production" ? "info" : "debug",
    },
    ajv: {
      customOptions: {
        removeAdditional: "all",
        coerceTypes: true,
        allErrors: true,
      },
    },
  });

  await app.register(helmet);
  await app.register(cors, {
    origin: config.corsOrigins,
    credentials: true,
  });
  await app.register(rateLimit, {
    max: 300,
    timeWindow: "1 minute",
  });

  registerRequestContextHooks(app);
  registerErrorHandler(app);

  const container = createContainer(config);

  registerHealthRoutes(app, container);

  await app.register(async (protectedApp) => {
    registerAuthHook(protectedApp, container.auth);
    registerMetadataRoutes(protectedApp, container);
    registerRecordRoutes(protectedApp, container);
  });

  app.addHook("onClose", async () => {
    await container.close();
  });

  return app;
}
```

- [ ] **Step 4: Add the dev keypair generation script, `scripts/generate-dev-jwt-keypair.mjs`**

```js
import { generateKeyPairSync } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";

const { publicKey, privateKey } = generateKeyPairSync("rsa", {
  modulusLength: 2048,
  publicKeyEncoding: { type: "spki", format: "pem" },
  privateKeyEncoding: { type: "pkcs8", format: "pem" },
});

mkdirSync("keys", { recursive: true });
writeFileSync("keys/dev-jwt-public.pem", publicKey);
writeFileSync("keys/dev-jwt-private.pem", privateKey);

console.log("Generated dev JWT keypair in ./keys (gitignored).");
console.log("Set in .env: AUTH_JWT_PUBLIC_KEY_PATH=./keys/dev-jwt-public.pem");
```

- [ ] **Step 5: Add the pnpm script — modify `package.json`**

Add to `"scripts"`:
```json
    "auth:dev-keys": "node scripts/generate-dev-jwt-keypair.mjs",
```

- [ ] **Step 6: Ignore the generated keys — modify `.gitignore`**

Add a line:
```
keys/
```

- [ ] **Step 7: Update `.env.example`**

Change:
```
AUTH_JWT_PUBLIC_KEY_PATH=
```
to:
```
AUTH_JWT_PUBLIC_KEY_PATH=./keys/dev-jwt-public.pem
```

- [ ] **Step 8: Generate your own local dev keypair and update your local `.env`**

Run: `pnpm auth:dev-keys`

Then edit your local `.env` (not tracked by git) to set:
```
AUTH_JWT_PUBLIC_KEY_PATH=./keys/dev-jwt-public.pem
```

- [ ] **Step 9: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 10: Commit**

```bash
git add src/server/config.ts src/core/container.ts src/server/app.ts scripts/generate-dev-jwt-keypair.mjs package.json pnpm-lock.yaml .gitignore .env.example
git commit -m "Wire JWT auth, request context, and error handling into the app"
```

---

### Task 6: CrudService context parameter, route wiring, and end-to-end verification

**Files:**
- Modify: `src/core/crud/crud-service.ts`
- Modify: `src/server/routes/records.ts`

**Interfaces:**
- Consumes: `sendServiceError` from `src/server/error-handler.ts` (Task 3). `request.context` from Task 4.
- Produces: `CrudService.list(entityName: string, input: ListInput, context: RequestContext)`, `CrudService.create(entityName: string, rawData: Record<string, unknown>, context: RequestContext)` — `defaultContext()` is deleted.

- [ ] **Step 1: Remove `defaultContext()` and add the `context` parameter — modify `src/core/crud/crud-service.ts`**

Change the `list` method signature and body:
```ts
  async list(
    entityName: string,
    input: ListInput,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto[]>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = this.permissions.canReadEntity(context, entity.name);
```
(remove the `const context = this.defaultContext();` line that used to precede this)

Change the `create` method signature and body the same way:
```ts
  async create(
    entityName: string,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = this.permissions.canCreateEntity(context, entity.name);
```
(remove its `const context = this.defaultContext();` line too)

Delete the `defaultContext()` private method entirely (it no longer has any callers).

- [ ] **Step 2: Thread `request.context` and the structured error helper through the routes — modify `src/server/routes/records.ts`**

Add the import:
```ts
import { sendServiceError } from "../error-handler";
```

Change the GET handler body:
```ts
    async (request, reply) => {
      const query = ListQuerySchema.parse(request.query);
      const result = await container.crud.list(request.params.entity, query, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data, page: result.page };
    },
```

Change the POST handler body:
```ts
    async (request, reply) => {
      const body = RecordBodySchema.parse(request.body);
      const result = await container.crud.create(request.params.entity, body.data, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return reply.code(201).send({ data: result.data });
    },
```

- [ ] **Step 3: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no errors.

- [ ] **Step 4: End-to-end manual verification**

Bring up dependencies and migrate (skip if already running from a previous session):
```bash
docker compose up -d postgres rabbitmq
pnpm db:migrate
```

Start the app in one terminal:
```bash
pnpm dev
```

In another terminal, confirm `/health` is public:
```bash
curl -i http://localhost:3000/health
```
Expected: `200`, body `{"status":"ok","checks":{"database":true}}`.

Confirm a protected route rejects an unauthenticated request with the structured shape:
```bash
curl -i http://localhost:3000/metadata/entities
```
Expected: `401`, body like `{"error":{"code":"unauthorized","message":"...","requestId":"...","traceId":"..."}}`, and response headers `x-request-id` / `x-trace-id` present.

Mint a token signed with your dev private key:
```bash
node -e "
const jwt = require('jsonwebtoken');
const fs = require('fs');
const privateKey = fs.readFileSync('keys/dev-jwt-private.pem', 'utf8');
const token = jwt.sign(
  { tenantId: '00000000-0000-0000-0000-000000000001', roles: ['admin'] },
  privateKey,
  { algorithm: 'RS256', subject: '00000000-0000-0000-0000-000000000002' },
);
console.log(token);
"
```

Use the printed token (`TOKEN` below) to confirm the protected routes now work end to end:
```bash
curl -i -H "Authorization: Bearer TOKEN" http://localhost:3000/metadata/entities

curl -i -X POST \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"data":{"code":"C001","name":"Acme"}}' \
  http://localhost:3000/api/crm.customers

curl -i -H "Authorization: Bearer TOKEN" http://localhost:3000/api/crm.customers
```
Expected: `200` on metadata, `201` with the created customer record on POST, `200` with a list containing that record on the final GET.

- [ ] **Step 5: Run the full test suite one more time**

Run: `pnpm vitest run && pnpm typecheck`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add src/core/crud/crud-service.ts src/server/routes/records.ts
git commit -m "Thread RequestContext through CrudService and record routes"
```
