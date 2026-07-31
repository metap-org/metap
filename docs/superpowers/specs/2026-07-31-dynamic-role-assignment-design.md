# Dynamic Role Assignment — Design

Date: 2026-07-31
Status: Approved, pending implementation plan

## Context

This is sub-project 1 of a 4-part initiative implementing roadmap Phase 3
("Permission Engine") with a dynamic (DB-backed, runtime-editable) model,
per explicit request rather than the roadmap's more modest RBAC+ABAC
scaffold:

1. **Dynamic role assignment** (this spec) — who has which role, resolved
   from the database instead of baked into a JWT.
2. Policy storage + RBAC/ABAC evaluator — field/record rules stored in DB,
   replacing static `EntityPermissions` declarations.
3. Field-level + record-level enforcement — wiring the evaluator into
   `CrudService` and `QueryPlanner`.
4. `PolicyExplainer` + `PermissionSnapshotCache` + policy tests.

Sub-projects 2-4 each get their own spec/plan cycle once this one ships.

Today, `roles` is a claim baked into the JWT at mint time
(`scripts/mint-dev-token.mjs`) and copied verbatim into `RequestContext` by
`buildRequestContext` (`src/core/auth/request-context.ts`). There is no
database record of who has which role — granting or revoking access means
minting a new token, and there is no way to revoke an already-issued
token's effective permissions before it expires. This is adequate for a
single-tenant dev harness but not for "dynamic" permission management.

## Goals

- Roles are assigned to `(tenantId, userId)` pairs in the database and can
  be granted/revoked at runtime, taking effect on the *next* request (no
  re-login, no waiting for token expiry).
- JWT is reduced to an identity assertion (`sub`, `tenantId`, optional
  `functionId`) — it no longer carries authorization data.
- A minimal admin API exists to assign, revoke, and list role assignments.
- A bootstrap path exists to create the first admin without going through
  the (admin-gated) API.

## Non-goals

- Policy/rule storage (which role can do what) — sub-project 2.
- Field-level/record-level enforcement — sub-project 3.
- Caching role lookups — sub-project 4. This round accepts one DB query
  per authenticated request as the cost of correctness; revisit only if
  it's measured to matter.
- A `roles` catalog table (canonical list of valid role names with
  descriptions). Role names stay free-form strings, same as today (e.g.
  `"admin"`) — nothing currently needs to enumerate "all roles that
  exist," so a catalog would be speculative.
- A `users` table / user profiles. `userId` remains an opaque UUID (the
  JWT `sub`); `GET /admin/users` only surfaces users that have at least
  one role assignment, grouped from `user_roles` — there is nowhere else
  to learn a user exists.

## Design

### 1. Auth flow

`src/core/auth/jwt-verifier.ts` — `ClaimsSchema` drops the `roles` field
entirely:

```ts
const ClaimsSchema = z.object({
  sub: z.string().min(1),
  tenantId: z.string().min(1),
  functionId: z.string().optional(),
  exp: z.number(),
});
```

`src/server/plugins/auth-hook.ts` — the `onRequest` hook becomes an async
function (Fastify supports `async (request, reply) => {...}` hooks
natively; the current callback/`done()` style is replaced, not layered
on top). After verifying the token, it calls the new
`RoleAssignmentService.getRolesForUser(tenantId, userId)` and merges the
result into the context it builds:

```ts
instance.addHook("onRequest", async (request) => {
  const header = request.headers.authorization;
  if (!header || !header.startsWith(BEARER_PREFIX)) {
    throw new AuthError("Missing or invalid authorization header.");
  }
  const claims = verifier.verify(header.slice(BEARER_PREFIX.length));
  const roles = await roleAssignments.getRolesForUser(claims.tenantId, claims.sub);
  request.context = buildRequestContext(claims, roles);
});
```

`buildRequestContext` gains a second parameter (`roles: readonly
string[]`) instead of reading `claims.roles`.

`scripts/mint-dev-token.mjs` drops its `roles` CLI argument — a minted
token now only encodes `tenantId` and `userId` (subject).

### 2. Schema: `user_roles`

New Drizzle table in `src/infra/db/schema.ts`, following the existing
`workflow_events`-style append/remove table (not append-only — revoking a
role deletes the row):

- `id` (uuid, pk)
- `tenantId` (uuid)
- `userId` (uuid)
- `role` (varchar(80))
- `createdAt` (timestamptz, default now)
- `createdBy` (uuid, nullable — who assigned it)

Unique constraint on `(tenantId, userId, role)` (prevents duplicate
assignment; also gives `assignRole` a natural `ON CONFLICT DO NOTHING`
idempotency path). Index on `(tenantId, userId)` — this is the hot path,
queried on every authenticated request.

### 3. `RoleAssignmentService`

New file `src/core/auth/role-assignment-service.ts`:

- `getRolesForUser(tenantId: string, userId: string): Promise<string[]>`
- `assignRole(tenantId: string, userId: string, role: string, assignedBy: string | undefined): Promise<void>` — upsert via `ON CONFLICT (tenantId, userId, role) DO NOTHING`.
- `revokeRole(tenantId: string, userId: string, role: string): Promise<void>` — delete matching row; no error if it didn't exist (revoking a role you don't have is a no-op, not a failure).
- `listUsers(tenantId: string): Promise<{ userId: string; roles: string[] }[]>` — groups `user_roles` rows by `userId` within the tenant.

Wired into `container.ts` alongside the other core services, constructed
with the `Database` handle.

### 4. Admin API

New file `src/server/routes/admin.ts`, registered like the other route
files. Every route requires `context.roles.includes("admin")`, checked
inline at the top of each handler (this sub-project doesn't yet have a
general policy evaluator — that's sub-project 2/3 — so this is a direct
check, same style as `PermissionService`'s existing `admin` bypass) — a
non-admin caller gets 403 `forbidden`, matching the existing
`SERVICE_ERROR_MESSAGES` convention. All queries are scoped to
`context.tenantId`.

- `GET /admin/users` → `{ data: { userId, roles }[] }`
- `GET /admin/users/:userId/roles` → `{ data: string[] }`
- `POST /admin/users/:userId/roles` body `{ role: string }` → calls `assignRole` then `getRolesForUser`, returns `{ data: string[] }` (the user's roles after the change)
- `DELETE /admin/users/:userId/roles/:role` → calls `revokeRole` then `getRolesForUser`, returns `{ data: string[] }` (the user's roles after the change)

### 5. Bootstrap

New script `scripts/seed-admin.mjs` (`pnpm seed:admin <tenantId>
<userId>`), following the existing `scripts/mint-dev-token.mjs` /
`scripts/generate-dev-jwt-keypair.mjs` pattern: a standalone script using
the same DB connection env var, inserting one row directly into
`user_roles` with `role = "admin"`. Documented as a one-time dev-setup
step. Not exposed over HTTP.

## Consequences for existing tests

Every existing live-DB test that currently mints a token with `roles:
["admin"]` embedded (`src/server/plugins/auth-hook.test.ts`,
`src/core/auth/jwt-verifier.test.ts`, `src/core/crud/crud-service.test.ts`,
`src/server/app.test.ts`) breaks, because `roles` is no longer read from
the JWT — after this change those tests would authenticate successfully
but resolve to zero roles (no matching `user_roles` rows), and every
permission check that currently passes via the `admin` bypass will start
failing with 403.

The implementation plan must add a shared test helper (e.g. insert a
`user_roles` row for the test's `tenantId`/`userId` in each suite's
`beforeAll`, cleaned up in `afterAll`) and update every affected test file
to use it instead of relying on the JWT's `roles` claim. This is expected
to be one of the larger tasks in the plan, not an afterthought — it
touches four existing test files across the codebase.

## Open items for implementation plan

- Exact Drizzle migration for `user_roles` (via `pnpm db:generate` /
  `db:migrate`).
- Whether `admin.ts` routes need their own Zod body/param schemas
  following the existing `records.ts`/`error-handler.ts` conventions
  (yes — same pattern, `zodToJsonSchema`).
- The shared "seed a role in the DB for this test" helper's exact shape
  and where it lives (likely a small helper module under `src/core/auth/`
  or inlined per test file, following whatever's least duplicative given
  how many suites need it).
