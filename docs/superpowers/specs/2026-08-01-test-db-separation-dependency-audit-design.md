# Test DB Separation + Dependency Audit — Design

Date: 2026-08-01
Status: Approved, pending implementation plan

## Context

Two unrelated but bundled infra-hygiene requests:

1. Integration tests (every `*.test.ts` file suffixed "(live DB)" in this
   repo) currently connect to the same `metap` database on the shared
   postgres container that `pnpm dev` also uses. Nothing prevents a test
   run from colliding with data a developer is manually poking at via
   curl/the dev server — which has literally happened during this session
   (manual verification records and live-DB test records sharing one
   database). Tests need their own database.
2. `package.json`'s dependency versions have drifted behind npm's
   `latest` (some, like `drizzle-orm`, were already bumped ad hoc outside
   any of this session's plans — visible as an unexplained lockfile diff
   earlier in this session). An explicit audit-and-bump pass is overdue,
   plus a `engines.node` upper bound that doesn't exist today.

This is infra/tooling work, not a feature — no user-facing behavior
changes. It precedes (and is unrelated to) the separate bugfix plan for
the two permission-engine bugs recorded in project memory
(`project_permission_engine_known_bugs.md`), which comes after this.

## Goals

- Every live-DB test connects to a dedicated `metap_test` database,
  distinct from the `metap` database `pnpm dev`/`pnpm worker:outbox` use
  — a test run can never collide with dev-server data again.
- `engines.node` gets an explicit upper bound: `">=24.0.0 <26.0.0"` (node
  24.x and 25.x allowed, 26+ blocked) — today it's `">=24.15.0"` with no
  ceiling.
- Every dependency in `package.json` is audited against npm `latest` and
  bumped, **except** `typescript`, which stays on its 5.x line (latest
  5.x patch) — `typescript@latest` is 7.0.2, a from-scratch native (Go)
  compiler rewrite; jumping to it risks breaking `typescript-eslint` and
  the rest of the type-checking toolchain in ways disproportionate to
  this round's scope.
- `amqplib` bumps to 2.x, which ships its own bundled TypeScript types
  (`"types": "./index.d.ts"` in its own `package.json`) — `@types/amqplib`
  (currently a devDependency, capped at 0.10.x on npm, incompatible with
  amqplib 2.x) is removed. This is very likely what actually resolves the
  pre-existing `src/infra/messaging/rabbitmq.ts` typecheck errors this
  entire session has been treating as "known, pre-existing, out of
  scope" — they were never a bug in `rabbitmq.ts`, they were `@types/amqplib`
  being stale against the already-installed amqplib runtime version.

## Non-goals

- Fixing the two permission-engine bugs — separate plan, after this one.
- Any behavior change to `PermissionService`/`CrudService`/etc. — this
  round only touches `package.json`, lockfile, test DB config, and
  whatever source changes are *forced* by a dependency's breaking API
  change (e.g. `rabbitmq.ts` if amqplib 2.x's API differs from 0.10.x).
- Converting any existing "(live DB)" test into a mocked/no-DB unit test.
  Every current DB-touching test genuinely exercises real SQL/transaction/
  persistence behavior — that's what makes it an integration test, and
  none of them are mislabeled. "Unit tests must never touch a real DB" is
  satisfied today already (`policy-condition.test.ts`,
  `policy-explainer.test.ts` are the only pure-logic suites, and neither
  touches a DB); this round's job is only to give the *integration* tests
  their own database, not to reclassify anything.
- A separate postgres container/port for tests — decided against in favor
  of a second database on the existing container.

## Design

### 1. `metap_test` database

`docker-compose.yml`'s `postgres` service gets a new bind mount:

```yaml
    volumes:
      - metap-postgres:/var/lib/postgresql/data
      - ./docker/postgres-init:/docker-entrypoint-initdb.d
```

New file `docker/postgres-init/01-create-test-db.sql`:

```sql
CREATE DATABASE metap_test;
```

Postgres only runs `/docker-entrypoint-initdb.d/*` scripts the *first*
time a volume initializes — irrelevant for the `metap-postgres` volume
already running in this environment. The implementation plan must also
run `CREATE DATABASE metap_test;` once, directly, against the live
container (a plain additive `CREATE DATABASE`, not a destructive
operation — existing data in `metap` is untouched). The init script is
for every future fresh clone/volume, not a substitute for that one-time
step here.

### 2. `TEST_DATABASE_URL`

New env var, `.env.example` gains:

```
TEST_DATABASE_URL=postgres://metap:metap@localhost:5433/metap_test
```

Every live-DB test file's current line:

```ts
const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";
```

becomes:

```ts
const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
```

`rabbitmqUrl` (also present in several of these files, reading
`RABBITMQ_URL`) is untouched — RabbitMQ has no equivalent "which database"
concern; sharing the broker between dev and test is a pre-existing,
separate, and much lower-risk situation (queues aren't stateful the way a
Postgres database with real rows is) and is out of scope here.

### 3. Migrations against `metap_test`

New `package.json` script:

```json
"db:migrate:test": "DATABASE_URL=postgres://metap:metap@localhost:5433/metap_test drizzle-kit migrate"
```

`drizzle.config.ts` reads `process.env.DATABASE_URL` (unchanged — it's the
one place in this repo that's already parameterized this way); this
script just overrides that one env var for a single invocation, pointing
migrations at `metap_test` instead of the dev database. Nothing about
`TEST_DATABASE_URL` is involved here — that variable is only read by the
test files themselves (§2), not by drizzle-kit.

### 4. `engines.node`

```json
"engines": {
  "node": ">=24.0.0 <26.0.0"
}
```

### 5. Dependency bump targets

All versions below are npm `latest` as of 2026-08-01, except `typescript`:

| Package | Target |
|---|---|
| `fastify` | 5.11.0 |
| `@fastify/cors` | 11.3.0 |
| `@fastify/helmet` | 13.1.0 |
| `@fastify/rate-limit` | 11.2.0 |
| `dotenv` | 17.4.2 |
| `pino` | 10.3.1 |
| `zod` | 4.4.3 |
| `amqplib` | 2.0.1 |
| `zod-to-json-schema` | latest at bump time (already declares `peerDependencies: { zod: "^3.25.28 \|\| ^4" }`, confirmed zod 4-compatible) |
| `@eslint/js` | 10.0.1 |
| `eslint` | 10.8.0 |
| `eslint-config-prettier` | 10.1.8 |
| `globals` | 17.8.0 |
| `drizzle-kit` | 0.31.10 |
| `@types/pg` | 8.20.2 |
| `@types/node` | latest **24.x** (not 26.x — pinned to match `engines.node`'s 24/25 range, avoids typing against Node APIs that don't exist in the pinned runtime range) |
| `typescript` | latest **5.x** (5.9.3) — explicit exception, see Goals |
| `@types/amqplib` | **removed** — amqplib 2.x ships its own types |

Packages not listed (`drizzle-orm`, `jsonwebtoken`, `@types/jsonwebtoken`,
`tsup`, `tsx`, `typescript-eslint`, `prettier`, `vitest`... — `vitest`
*is* listed, see below) are already at their npm-latest and need no
version bump; `vitest` is one of the packages that does move — 2.1.9 →
4.1.10 (two majors; its v4 peer dependencies are all optional/`*` except
`@types/node` which must satisfy `^20 || ^22 || >=24` — the 24.x pin
above satisfies this).

### 6. Handling breaking changes

Zod 4, amqplib 2, and the various `@fastify/*` majors each carry real API
changes. This spec does not enumerate them — the correct approach is
bump, run `pnpm typecheck && pnpm test && pnpm lint`, and fix whatever
breaks, file by file, using the compiler/test failures as the source of
truth rather than guessing at a changelog. The implementation plan should
budget explicit steps for this per package (at minimum: zod, amqplib,
each `@fastify/*` package, eslint), not treat "bump the numbers" as the
whole task.

## Consequences for existing code

- Every "(live DB)" test file's `databaseUrl` line changes (see §2) —
  mechanical, one line each, no test logic changes.
- `rabbitmq.ts` almost certainly needs real changes to match amqplib
  2.x's API — this is expected to *fix* pre-existing typecheck errors,
  not introduce new ones, but the exact diff can't be known until the
  bump is done and the compiler is consulted.
- Any `zod` v4 API changes affecting schema definitions across
  `src/core/metadata/entity.ts`, every route's Zod schemas, and
  `PolicyConditionSchema`'s recursive `z.lazy` construction in
  `admin.ts` need to be found via typecheck, not assumed.

## Open items for implementation plan

- Whether `pnpm install` after the `package.json` bump resolves cleanly
  in one pass or needs per-package incremental bumps to isolate which
  bump caused which breakage (recommend incremental: bump in small
  batches — e.g. tooling first (eslint family, @types/node, drizzle-kit),
  then runtime deps (fastify family, pino, dotenv), then the two riskiest
  (zod, amqplib) last and separately — so failures are attributable).
- Exact `rabbitmq.ts` rewrite needed for amqplib 2.x, determined by
  typecheck output once bumped.
