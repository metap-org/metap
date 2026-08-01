# Test DB Separation + Dependency Audit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give integration tests their own `metap_test` database (separate from the `metap` dev database `pnpm dev` uses), add an `engines.node` upper bound, and bump every dependency in `package.json` to npm `latest` — except `typescript`, pinned to its 5.x line — fixing whatever breaking changes surface along the way.

**Architecture:** A second Postgres database (`metap_test`) on the existing `postgres` container, selected via a new `TEST_DATABASE_URL` env var that every live-DB test file reads instead of `DATABASE_URL`. Dependency bumps happen in risk-ordered batches (low-risk tooling → runtime deps → the two highest-risk majors, zod and amqplib, last and separately) so a failure is attributable to a specific batch.

**Tech Stack:** Docker Compose, PostgreSQL, pnpm, Drizzle Kit, vitest.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-01-test-db-separation-dependency-audit-design.md`.
- No behavior changes to application logic — only test config, `package.json`/lockfile, and whatever source changes a dependency's breaking API change forces (expected: `src/infra/messaging/rabbitmq.ts` for amqplib 2.x).
- `typescript` stays on its 5.x line (latest 5.x patch) — do not bump to 7.x. This is an intentional, explicit exception to "bump everything to latest."
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for review.
- `docker compose up -d postgres rabbitmq` must be running throughout.
- After every dependency batch: run `pnpm typecheck && pnpm lint && pnpm test`. Fix what breaks before moving to the next batch — don't stack unverified bumps.

---

### Task 1: `metap_test` database + `TEST_DATABASE_URL`

**Files:**
- Modify: `docker-compose.yml`
- Create: `docker/postgres-init/01-create-test-db.sql`
- Modify: `.env.example`
- Modify: `package.json` (add `db:migrate:test` script)

**Interfaces:**
- Produces: a `metap_test` database on the existing postgres container; `TEST_DATABASE_URL` env var — consumed by Task 2's test files.

- [ ] **Step 1: Add the postgres init-script bind mount**

In `docker-compose.yml`, change:

```yaml
    volumes:
      - metap-postgres:/var/lib/postgresql/data
```

to:

```yaml
    volumes:
      - metap-postgres:/var/lib/postgresql/data
      - ./docker/postgres-init:/docker-entrypoint-initdb.d
```

- [ ] **Step 2: Add the init script**

Create `docker/postgres-init/01-create-test-db.sql`:

```sql
CREATE DATABASE metap_test;
```

(This only runs automatically the first time a fresh `metap-postgres` volume initializes — i.e. for anyone cloning this repo from now on. It does not retroactively run against the volume already in use in this environment — Step 3 handles that.)

- [ ] **Step 3: Create the database on the already-running container**

Run: `docker compose up -d postgres` (ensure it's running), then:

```bash
docker compose exec postgres psql -U metap -d metap -c "CREATE DATABASE metap_test;"
```

Expected: `CREATE DATABASE`. If it errors with "already exists," that's fine — someone already ran this step; continue.

Verify: `docker compose exec postgres psql -U metap -l` — expect both `metap` and `metap_test` listed.

- [ ] **Step 4: Add `TEST_DATABASE_URL` to `.env.example`**

In `.env.example`, add this line after the existing `DATABASE_URL=...` line:

```
TEST_DATABASE_URL=postgres://metap:metap@localhost:5433/metap_test
```

- [ ] **Step 5: Add the `db:migrate:test` script**

In `package.json`, change:

```json
    "db:migrate": "drizzle-kit migrate",
```

to:

```json
    "db:migrate": "drizzle-kit migrate",
    "db:migrate:test": "DATABASE_URL=postgres://metap:metap@localhost:5433/metap_test drizzle-kit migrate",
```

- [ ] **Step 6: Apply migrations to `metap_test`**

Run: `pnpm db:migrate:test`
Expected: exits 0, all existing migrations applied.

Verify: `docker compose exec postgres psql -U metap -d metap_test -c '\dt'` — expect the same tables as `metap` (`records`, `outbox_events`, `workflow_events`, `user_roles`, `policies`).

---

### Task 2: Point every live-DB test at `metap_test`

**Files:**
- Modify: `src/core/auth/role-assignment-service.test.ts`
- Modify: `src/core/crud/crud-service.test.ts`
- Modify: `src/core/permission/permission-snapshot.test.ts`
- Modify: `src/core/permission/permission-service.test.ts`
- Modify: `src/core/query/query-planner.test.ts`
- Modify: `src/server/routes/admin.test.ts`
- Modify: `src/server/app.test.ts`

**Interfaces:**
- Consumes: `TEST_DATABASE_URL` (Task 1).

- [ ] **Step 1: Update each file's `databaseUrl` fallback**

In each of the seven files listed above, every occurrence (note:
`src/server/app.test.ts` has **two** — one per `describe` block) of:

```ts
const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";
```

becomes:

```ts
const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
```

`rabbitmqUrl` lines (present in several of these files) are untouched —
out of scope per the spec.

- [ ] **Step 2: Run the full test suite against `metap_test`**

Run: `pnpm test`
Expected: all tests pass, same counts as before this change (this is a
pure re-pointing — the tests themselves are unchanged, and `metap_test`
now has the same schema as `metap` per Task 1's migration).

- [ ] **Step 3: Confirm isolation**

Run:

```bash
docker compose exec postgres psql -U metap -d metap -c "SELECT count(*) FROM records;"
docker compose exec postgres psql -U metap -d metap_test -c "SELECT count(*) FROM records;"
```

Expected: `metap`'s count reflects only whatever's been manually created
there (unaffected by the test run just performed); `metap_test` may have
a small nonzero count if any test's cleanup path was skipped, but should
not have grown `metap`'s row count. This is the concrete proof the
separation works.

---

### Task 3: `engines.node` upper bound

**Files:**
- Modify: `package.json`

- [ ] **Step 1: Add the upper bound**

In `package.json`, change:

```json
  "engines": {
    "node": ">=24.15.0"
  },
```

to:

```json
  "engines": {
    "node": ">=24.0.0 <26.0.0"
  },
```

- [ ] **Step 2: Verify**

Run: `node --version` — confirm it's within `24.0.0`–`25.x` (this
environment runs 24.15.0, satisfies the new range). Run `pnpm install` —
expect no engine-mismatch warning.

---

### Task 4: Bump low-risk tooling dependencies

**Files:**
- Modify: `package.json`

**Interfaces:** none (dev tooling only).

- [ ] **Step 1: Update versions**

In `package.json`'s `devDependencies`, change:

```json
    "@eslint/js": "^9.17.0",
    "@types/amqplib": "^0.10.6",
    "@types/jsonwebtoken": "^9.0.10",
    "@types/node": "^22.10.2",
    "@types/pg": "^8.11.10",
    "drizzle-kit": "^0.30.1",
    "eslint": "^9.17.0",
    "eslint-config-prettier": "^9.1.0",
    "globals": "^15.14.0",
    "prettier": "^3.4.2",
    "tsup": "^8.3.5",
    "tsx": "^4.19.2",
    "typescript": "^5.7.2",
    "typescript-eslint": "^8.18.2",
    "vitest": "^2.1.8"
```

to:

```json
    "@eslint/js": "^10.0.1",
    "@types/amqplib": "^0.10.6",
    "@types/jsonwebtoken": "^9.0.10",
    "@types/node": "^24.13.3",
    "@types/pg": "^8.20.2",
    "drizzle-kit": "^0.31.10",
    "eslint": "^10.8.0",
    "eslint-config-prettier": "^10.1.8",
    "globals": "^17.8.0",
    "prettier": "^3.4.2",
    "tsup": "^8.3.5",
    "tsx": "^4.19.2",
    "typescript": "^5.9.3",
    "typescript-eslint": "^8.18.2",
    "vitest": "^2.1.8"
```

(`@types/amqplib` stays for now — Task 7 removes it when amqplib itself
bumps. `@types/node`'s exact patch — `24.13.3` as of this plan's writing —
should be double-checked against `npm view @types/node@24 version` at
execution time in case a newer 24.x patch has shipped since; use whatever
that command returns, staying on the `24.x` major. `typescript` moves to
its latest 5.x patch (`5.9.3`) — **not** to `latest` (`7.0.2`), per the
spec's explicit exception. `prettier`/`tsup`/`tsx`/`typescript-eslint` are
listed here unchanged because they're already at npm-latest — included
only so this step's before/after blocks show the complete
`devDependencies` object, not because they need editing. `vitest` is
deliberately **not** bumped in this step — it's the test runner itself,
bumping it two majors (2.1.8 → 4.x) carries more risk than the rest of
this batch and gets its own dedicated Step 3 below so a vitest-specific
failure isn't conflated with an eslint-config-prettier one.)

- [ ] **Step 2: Install and verify**

Run: `pnpm install`
Expected: resolves without errors.

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new failures beyond the already-known pre-existing
`rabbitmq.ts` typecheck errors (Task 7 addresses those). ESLint 10 / `@eslint/js` 10 / `eslint-config-prettier` 10 occasionally change recommended-rule defaults between majors — if `pnpm lint` reports genuinely new violations (not the pre-existing jsonb-cast/unused-import class already accepted throughout this codebase), fix them or, if a rule's default changed in a way that doesn't fit this codebase's conventions, note it rather than silently suppressing.

- [ ] **Step 3: Bump `vitest` separately and verify in isolation**

In `package.json`'s `devDependencies`, change:

```json
    "vitest": "^2.1.8"
```

to:

```json
    "vitest": "^4.1.10"
```

Run: `pnpm install`
Expected: resolves without errors (vitest 4's peer `@types/node: "^20.0.0 || ^22.0.0 || >=24.0.0"` is satisfied by the `24.13.3` pin from Step 1).

Run: `pnpm test`
Expected: every existing test still runs and passes with the same
counts as before this step. Vitest 3/4 changed some defaults between
major versions (e.g. around thread pool behavior, snapshot handling) —
this repo doesn't have a `vitest.config.ts` and relies entirely on
defaults (per CLAUDE.md), so if `pnpm test` reports config-shape errors
rather than just test failures, that's the signal something in the new
defaults needs an explicit override; consult the actual error, don't
guess.

---

### Task 5: Bump runtime dependencies (excluding zod, amqplib)

**Files:**
- Modify: `package.json`

- [ ] **Step 1: Update versions**

In `package.json`'s `dependencies`, change:

```json
    "@fastify/cors": "^10.0.1",
    "@fastify/helmet": "^12.0.1",
    "@fastify/rate-limit": "^10.1.1",
    "amqplib": "^0.10.5",
    "dotenv": "^16.4.7",
    "drizzle-orm": "^0.45.2",
    "fastify": "^5.2.1",
    "jsonwebtoken": "^9.0.3",
    "pg": "^8.13.1",
    "pino": "^9.5.0",
    "zod": "^3.24.1",
    "zod-to-json-schema": "^3.24.1"
```

to:

```json
    "@fastify/cors": "^11.3.0",
    "@fastify/helmet": "^13.1.0",
    "@fastify/rate-limit": "^11.2.0",
    "amqplib": "^0.10.5",
    "dotenv": "^17.4.2",
    "drizzle-orm": "^0.45.2",
    "fastify": "^5.11.0",
    "jsonwebtoken": "^9.0.3",
    "pg": "^8.13.1",
    "pino": "^10.3.1",
    "zod": "^3.24.1",
    "zod-to-json-schema": "^3.24.1"
```

(`amqplib` and `zod`/`zod-to-json-schema` are deliberately left unbumped
here — Tasks 6 and 7 handle them separately, each with its own
verification pass, per the spec's risk-isolation guidance. `drizzle-orm`,
`jsonwebtoken`, `pg` are already at latest — unchanged.)

- [ ] **Step 2: Install and verify**

Run: `pnpm install`
Expected: resolves without errors.

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new failures. `@fastify/cors`/`@fastify/helmet`/
`@fastify/rate-limit` major bumps occasionally change plugin option
shapes — if `src/server/app.ts`'s `app.register(helmet)` / `app.register(cors, {...})` / `app.register(rateLimit, {...})` calls no longer typecheck, fix the option objects to match the new major's types (consult the type error, not the changelog, per the spec's guidance) and re-run `pnpm test` to confirm the server still boots correctly (`app.test.ts` exercises `buildApp`).

---

### Task 6: Bump `zod` + `zod-to-json-schema`

**Files:**
- Modify: `package.json`
- Modify: whatever fails typecheck after the bump (expected candidates: `src/core/metadata/entity.ts`, `src/server/routes/records.ts`, `src/server/routes/admin.ts`, `src/core/query/query-planner.test.ts`'s schema usage if any — determined by the compiler, not guessed)

- [ ] **Step 1: Update versions**

In `package.json`'s `dependencies`, change:

```json
    "zod": "^3.24.1",
    "zod-to-json-schema": "^3.24.1"
```

to:

```json
    "zod": "^4.4.3",
    "zod-to-json-schema": "^3.25.2"
```

- [ ] **Step 2: Install**

Run: `pnpm install`
Expected: resolves without errors (`zod-to-json-schema@3.25.2` declares `peerDependencies: { zod: "^3.25.28 || ^4" }`, satisfied by zod 4.4.3).

- [ ] **Step 3: Typecheck and fix**

Run: `pnpm typecheck`
Expected: likely errors somewhere schemas are defined or `z.infer`/`z.ZodType` is used directly. Fix each reported error by consulting Zod 4's actual type signature at that call site — do not preemptively rewrite anything that isn't flagged. Pay particular attention to `src/server/routes/admin.ts`'s `PolicyConditionSchema` (`z.lazy` + the `z.ZodType<unknown>` workaround comment explaining a Zod 3 inference quirk around `z.unknown()` — Zod 4 may have changed this inference; if so, the workaround comment and cast may no longer be necessary, and removing them is preferable to leaving a stale comment).

- [ ] **Step 4: Run lint and tests**

Run: `pnpm lint && pnpm test`
Expected: pass. Fix any new failures the same way — compiler/test output as source of truth.

---

### Task 7: Bump `amqplib`, remove `@types/amqplib`, fix `rabbitmq.ts`

**Files:**
- Modify: `package.json`
- Modify: `src/infra/messaging/rabbitmq.ts`

- [ ] **Step 1: Update versions**

In `package.json`'s `dependencies`, change:

```json
    "amqplib": "^0.10.5",
```

to:

```json
    "amqplib": "^2.0.1",
```

In `devDependencies`, remove this line entirely:

```json
    "@types/amqplib": "^0.10.6",
```

- [ ] **Step 2: Install**

Run: `pnpm install`
Expected: resolves without errors.

- [ ] **Step 3: Typecheck and fix `rabbitmq.ts`**

Run: `pnpm typecheck`
Expected: the errors that have been present at
`src/infra/messaging/rabbitmq.ts` throughout every prior plan in this
session's history (`ChannelModel`/`Connection` mismatch,
`createChannel`/`close` not existing) either disappear entirely (amqplib
2.x's bundled types describing its actual runtime shape correctly) or
change in nature. Fix whatever `tsc` reports at this file now — the
current source is:

```ts
import amqp from "amqplib";

export function createRabbitPublisher(url: string) {
  let connection: amqp.Connection | undefined;
  let channel: amqp.Channel | undefined;

  async function getChannel() {
    if (!connection) {
      connection = await amqp.connect(url);
    }

    if (!channel) {
      channel = await connection.createChannel();
      await channel.assertExchange("metap.events", "topic", { durable: true });
    }

    return channel;
  }

  return {
    async publish(topic: string, payload: unknown) {
      const currentChannel = await getChannel();
      currentChannel.publish("metap.events", topic, Buffer.from(JSON.stringify(payload)), {
        contentType: "application/json",
        persistent: true,
      });
    },
    async close() {
      await channel?.close();
      await connection?.close();
    },
  };
}

export type RabbitPublisher = ReturnType<typeof createRabbitPublisher>;
```

If `tsc` now reports it clean as-is, no changes needed beyond the
dependency bump itself. If it reports new/different errors (e.g. amqplib
2.x renaming `Connection`/`Channel` types, or `connect`/`createChannel`
returning a different shape), fix by following the compiler's guidance —
likely candidates based on amqplib 2.x's channel-based API: the type
names `amqp.Connection`/`amqp.Channel` may need to become whatever
amqplib 2.x's bundled `.d.ts` actually exports (check
`node_modules/amqplib/index.d.ts` if the error message alone isn't
enough context).

- [ ] **Step 4: Runtime verification, not just typecheck**

Since this changes the actual RabbitMQ client library (not just its
types), a clean `tsc` isn't sufficient proof — confirm publishing still
works at runtime:

Run: `pnpm test` — the create-record tests in `app.test.ts` /
`crud-service.test.ts` exercise `CrudService.create`, which enqueues an
outbox row but doesn't itself publish to RabbitMQ synchronously. To
verify the publisher itself:

```bash
docker compose up -d rabbitmq
pnpm dev &
sleep 3
curl -s -X POST http://localhost:3000/api/crm.customers \
  -H "Authorization: Bearer $(pnpm mint-token)" -H "Content-Type: application/json" \
  -d '{"data":{"code":"AMQP1","name":"Amqplib Bump Test"}}'
pnpm worker:outbox &
sleep 2
```

Check the RabbitMQ management UI (`http://localhost:15672`, user/pass
`metap`/`metap`) or its logs for a published message on the
`metap.events` exchange, confirming the outbox worker's
`RabbitPublisher.publish` call succeeded end-to-end against the new
amqplib version. Stop both background processes and clean up the test
record afterward (`DELETE FROM records WHERE code = 'AMQP1';` and the
matching `outbox_events` row, against whichever database — `metap` if
`pnpm dev` used the dev `.env`, since this step exercises the real dev
server, not the test DB).

---

### Task 8: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Full suite, clean**

Run: `pnpm typecheck`
Expected: **zero errors** — this is the first point in this repo's
history (per everything observed across every prior plan this session)
where `rabbitmq.ts` is not carrying pre-existing errors. If any remain,
they must be genuinely new/unrelated, not the old known set (which
should no longer exist given Task 7).

Run: `pnpm lint`
Expected: clean, or only pre-existing non-`rabbitmq.ts` issues already
accepted throughout this codebase (the jsonb-cast class in
`crud-service.ts`, etc.) — compare against what was true before this
plan started if anything is ambiguous.

Run: `pnpm test`
Expected: all tests pass, entirely against `metap_test`.

- [ ] **Step 2: Confirm the version/engine changes landed**

Run: `cat package.json | grep -A2 '"engines"'` — expect
`">=24.0.0 <26.0.0"`.

Run: `pnpm outdated` — expect only `typescript` (intentionally pinned
below its npm `latest`) and anything that shipped a newer patch between
this plan's writing and execution to appear; no package should still be
a full major version behind except `typescript`.

- [ ] **Step 3: Manual smoke test of the dev server**

Run: `pnpm dev` (background), `curl http://localhost:3000/health` —
expect `{"status":"ok","checks":{"database":true}}`. Stop the dev
server afterward.

---

## Plan Self-Review Notes

- **Spec coverage:** §1 (`metap_test`) → Task 1. §2 (`TEST_DATABASE_URL`) → Tasks 1-2. §3 (migrations) → Task 1. §4 (`engines.node`) → Task 3. §5 (bump targets table) → Tasks 4-7. §6 (handling breaking changes) → the "consult the compiler, don't guess" guidance repeated in Tasks 5-7.
- **Gap found during self-review and fixed inline:** the spec's bump table explicitly lists `typescript` (pinned to 5.x latest) and implies `vitest` needs bumping too (it's discussed in the spec's §5 table text), but neither was actually included in any task's before/after `package.json` block in the first draft of this plan — an oversight caught by re-reading the actual current `package.json` against every task's diff before execution. Fixed by adding `typescript` to Task 4's batch and giving `vitest` its own isolated verification step (Task 4, Step 3), rather than silently dropping either.
- **No other placeholders:** every step has literal command/code; Tasks 6-7's "fix what the compiler reports" language is the spec's own explicitly-chosen approach for unknowable-in-advance breaking changes, not a shortcut.
