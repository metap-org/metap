# Auth + RequestContext + Structured Errors Kernel

Date: 2026-07-28

Status: approved

Scope: first of four planned Phase 1 kernel pieces, in this priority order:

1. **Auth + RequestContext + structured errors + request/trace id** (this spec)
2. `CrudService` + optimistic locking
3. `QueryPlanner` hardening (metadata-constrained filters, tenant scope, max limit)
4. `PermissionService` (real RBAC/ABAC)

Each gets its own design → plan → implementation cycle. This spec covers only item 1.

## Motivation

A prior audit of a legacy system with a similar architecture identified concrete, exploitable problems that this design directly avoids:

- The auth middleware's request context was built before the async `jwt.verify` callback resolved — deterministically missing user identity on every request, not just under rare timing.
- On verify failure, the code called a redirect but still fell through to build context and continue the request pipeline — downstream code ran after the response had already been decided.
- Auth was applied by convention (gateway route config), so a route could silently end up unauthenticated if someone forgot to wire it.

`CrudService` currently hardcodes a `defaultContext()` (`src/core/crud/crud-service.ts:118-124`) as a known placeholder. This spec replaces it with real, server-verified request context.

## Architecture

```
Fastify app
├─ onRequest (root, all routes): assign traceId + reqId → bind to child logger → set response headers
├─ setErrorHandler (root, all routes): format every thrown error into the structured shape
├─ GET /health                              — outside auth, public
└─ protected plugin scope (new Fastify child context)
    ├─ onRequest: JWT auth hook → builds RequestContext → request.context
    ├─ GET/POST /metadata/*
    └─ GET/POST /api/:entity
```

`/health` is registered directly on the root `app` and is never touched by the auth hook — it's excluded by not being inside the protected scope, not by an exemption flag. `/metadata/*` and `/api/:entity` are registered inside a `fastify.register(async (instance) => {...})` child context that has the auth hook applied via `instance.addHook('onRequest', ...)`. Any route registered inside that scope in the future is protected automatically; nothing needs to remember to opt in.

## Components

### `src/core/auth/jwt-verifier.ts`

- Reads the RS256 public key from `config.authJwtPublicKeyPath` once, at container construction time (`fs.readFileSync`).
- **The app must fail to start** if the path is unset or the file can't be read/parsed, in every environment including local dev. `AppConfig.authJwtPublicKeyPath` changes from `z.string().optional()` to `z.string()` (required) in `src/server/config.ts`.
- Exposes `verify(token: string): Claims`, implemented with `jsonwebtoken`'s **synchronous** `jwt.verify(token, publicKey, { algorithms: ["RS256"] })` (no callback passed). This is a structural fix, not a discipline fix: the synchronous form cannot leave a window where context is built before verification completes.
- `Claims` is validated with a small Zod schema requiring `sub` (string) and `tenantId` (string); `roles` (string array, defaults to `[]`) and `functionId` (optional string) are permissive.
- On any failure (expired, bad signature, missing required claim), throws `AuthError` (see below).

### `src/core/auth/errors.ts`

```ts
export class AuthError extends Error {
  readonly statusCode = 401;
  readonly code = "unauthorized";
}
```

A single error class/code is enough for now — the codebase doesn't yet distinguish "expired" from "malformed" for the client, and doing so isn't needed by anything downstream today.

### `buildRequestContext(claims: Claims): RequestContext`

Maps `sub` → `userId`, plus `tenantId`, `roles`, `functionId` directly onto the existing `RequestContext` type (`src/core/permission/permission-service.ts`) — no changes to that type are needed, since it already has exactly these fields.

### Auth hook (`src/server/plugins/auth-hook.ts`)

- Reads `Authorization: Bearer <token>`. Missing header, malformed header, or a throwing `verify()` all result in an `AuthError` being thrown (never a redirect).
- On success, calls `buildRequestContext` and assigns `request.context` (declared via `fastify.decorateRequest("context", null)` and typed through Fastify's module augmentation).

### Request id / trace id (root-level `onRequest` hook, `src/server/plugins/request-context.ts`)

- **requestId**: Fastify's built-in per-request id (`request.id`), generated fresh every hop. No custom logic needed.
- **traceId**: read from an incoming `x-trace-id` header if present, else generate a new one. This one distinct concept is for cross-service propagation (relevant once the outbox-driven workers or another service call into metap); requestId is not propagated.
- Both are echoed as response headers (`x-request-id`, `x-trace-id`) and bound into a child logger (`request.log = request.log.child({ requestId: request.id, traceId })`) so every log line in the request carries both.
- This hook runs at the root level, before the auth hook, and applies to **every** route including `/health` — trace id is a request-lifecycle concern, not an authorization concern.

### Structured error response (root-level `setErrorHandler`)

Every error funnels through one shape:

```json
{ "error": { "code": "unauthorized", "message": "...", "requestId": "...", "traceId": "..." } }
```

- `AuthError` → its own `statusCode`/`code`.
- Zod validation errors (`ZodError` instances, thrown from manual `.parse()` calls in routes) → 400, code `validation_failed`.
- `CrudService`'s existing `ServiceResult` failures → routes no longer hand-roll `reply.code(result.status).send({ error: result.error })`; a shared `sendServiceError(reply, request, result)` helper wraps the same failure into this shape (`code` = `result.error`, `message` = a static human-readable string keyed by code, falling back to the code itself).
- Anything else (unexpected exceptions) → logged in full server-side via `request.log.error(err)`, but the client gets a generic `internal_error` body with no stack/detail leakage.

### `CrudService` signature change

`defaultContext()` (`src/core/crud/crud-service.ts:118-124`) is deleted outright — not kept as a fallback. `list(entityName, input)` and `create(entityName, rawData)` both gain a required `context: RequestContext` parameter, threaded from `request.context` in the route handlers (`src/server/routes/records.ts`). `QueryPlanner` and `PermissionService` signatures are unchanged in this pass — real tenant/RBAC enforcement is item 4 in the priority list above; for now `QueryPlanner.planList`'s existing `Partial<RequestContext>` parameter simply always receives a fully-populated context.

Metadata routes (`src/server/routes/metadata.ts`) move inside the protected scope but don't consume `request.context` yet (they don't do any permission check today) — that wiring is also deferred to the `PermissionService` item.

## Dependency change

Add `jsonwebtoken` (+ `@types/jsonwebtoken`) to `package.json`. No other new dependencies.

## Testing (minimal — important cases only, not an exhaustive matrix)

- `jwt-verifier`: one valid-token-round-trip test, one rejection test (expired **or** wrong-key — whichever is simpler to construct with `node:crypto.generateKeyPairSync`), and the missing-`tenantId`-claim rejection (since tenant scope being mandatory is the one invariant this whole design exists to protect).
- Auth hook, via Fastify's `app.inject()`: no/invalid `Authorization` header against a protected route → structured 401 body; valid token → request reaches the handler with `request.context` populated.

No config-matrix testing, no exhaustive claim-shape fuzzing.

## Out of scope (deferred to later items in the priority list)

- Real tenant/RBAC/field/record permission logic (`PermissionService` stays allow-everything).
- Query filter/sort allowlisting (`QueryPlanner` unchanged beyond receiving real context).
- Optimistic locking / `version` column enforcement.
