# Monorepo Restructure, Sub-project 1: `packages/core`

Date: 2026-08-02

Status: approved

Scope: first of a 3-part monorepo restructure, decomposed during brainstorming (too large for a single spec):

1. **`pnpm-workspace.yaml` + `packages/core`** (this spec) — move the existing backend into a workspace package, no behavior change.
2. `packages/platform-react` + `apps/demo` — extract `web/src/platform/` into its own package; `web/` (minus `platform/`) becomes `apps/demo`, a real second consumer of the platform package via `workspace:*`.
3. *(Not now, deferred)* An actual multi-service backend split (`apps/<module>`, real deploy topology) — per `docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split", this has its own trigger ("the first time a second, genuinely separate module needs to exist as its own deployable unit") which hasn't fired yet (only `crm.customers` exists today). Explicitly out of scope for this restructure — packaging only, not deployment topology.

## Motivation

Today's repo is one flat backend package at the root plus a separate, non-workspace `web/` package installed and run independently. Both `docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split" and "Future Evolution: Frontend Platform Package" sections already anticipated this exact restructure and documented its target shape and triggers in advance — this sub-project pulls the trigger on the backend-packaging half (moving today's single package into `packages/core` inside a real pnpm workspace), without yet building a second deployable backend module (that's a separate, much larger, and currently untriggered decision — see Scope above).

## Design

### Workspace root

New `pnpm-workspace.yaml`:

```yaml
packages:
  - "packages/*"
  - "apps/*"
```

Root `package.json` becomes a thin workspace root:
- `private: true`, no `dependencies` of its own beyond what's genuinely shared across every package (for now: none identified — even `typescript`/`prettier` stay pinned per-package to avoid silently coupling package versions; if real duplication pain shows up later, hoisting is a one-line move, not a redesign).
- Every current script (`dev`, `build`, `start`, `worker:outbox`, `index:reconcile`, `typecheck`, `test`, `lint`, `lint:fix`, `format`, `format:check`, `db:generate`, `db:migrate`, `db:migrate:test`, `db:studio`, `auth:dev-keys`, `mint-token`, `seed:admin`) stays present at the root, each forwarding via `pnpm --filter @metap/core <script>` — e.g. `"dev": "pnpm --filter @metap/core dev"`. Nobody's daily command changes; `CLAUDE.md`'s existing Commands section stays accurate without rewriting user habits.
- `engines`/`packageManager` fields move to `packages/core/package.json` (they describe the backend runtime, not the workspace itself) — though `packageManager` is also commonly kept at the workspace root since pnpm reads it there for corepack; keeping it at root too is harmless duplication-free (single source, root is where pnpm actually looks first).

Root keeps: `docker-compose.yml`, `docker/`, `.gitignore`, `.prettierrc.json`, `.prettierignore`, `README.md`, `CLAUDE.md`, `docs/`, `.claude/`, `.superpowers/` — these describe the whole repo/infra, not one package.

**Corrections found during implementation:**
- `.env`/`.env.example` and `keys/` move into `packages/core/` too, not stay at root as originally planned above — `src/server/config.ts` does a bare `import "dotenv/config"` (cwd-relative `.env` lookup) and the scripts read `keys/*.pem` via plain relative paths, both resolved against `process.cwd()`. Since `pnpm --filter @metap/core <script>` runs with cwd set to `packages/core/`, both need to live there for the existing (unchanged) code to keep finding them. `docker-compose.yml` doesn't read `.env` at all (its Postgres/RabbitMQ credentials are hardcoded inline), so this doesn't affect where Docker is invoked from.
- `.prettierignore` also moves into `packages/core/`, not stay at root. Unlike `.prettierrc.json` (whose style-rule resolution searches upward through parent directories, so it works fine staying at root), `.prettierignore`'s gitignore-style patterns are resolved relative to the ignore file's *own* directory, not the directory being scanned — confirmed via `prettier --file-info` returning `"ignored": false` for a migration-metadata JSON file that should have matched, until the ignore file was moved to live alongside the scanned tree. Pointing at it from elsewhere via `--ignore-path ../../.prettierignore` does not work around this; it has to be colocated.

### `packages/core`

Everything backend-specific moves here **as one atomic directory move**, preserving internal relative paths exactly (nothing inside `src/` changes):
- `src/` → `packages/core/src/`
- `scripts/` → `packages/core/scripts/`
- `keys/` → `packages/core/keys/`
- `tsconfig.json` → `packages/core/tsconfig.json`
- `drizzle.config.ts` → `packages/core/drizzle.config.ts`
- `eslint.config.js` → `packages/core/eslint.config.js`

New `packages/core/package.json`, named `@metap/core`, carrying every backend-only `dependency`/`devDependency` from today's root `package.json` (Fastify, Drizzle, Zod, `pino`, `jsonwebtoken`, `amqplib`, `vitest`, `tsx`, `tsup`, `eslint`, `drizzle-kit`, etc.) and the *unprefixed* versions of every script (`"dev": "tsx watch src/main.ts"`, etc. — identical to today's root scripts, just living one level down).

`tsconfig.json`'s `include: ["src", "drizzle.config.ts"]` and `outDir: "dist"` stay relative to `packages/core/`, so they need no path changes — only their file's own location changes.

Single lockfile: the root `pnpm-lock.yaml` continues to be the one lockfile for the whole workspace (standard pnpm workspace behavior) — `web/`'s separate lockfile is untouched by this sub-project (handled in sub-project 2, when `web/` itself joins the workspace).

### `CLAUDE.md` updates

Every backend file-path reference (`src/core/...`, `src/server/...`, `src/infra/...`, etc.) gets a `packages/core/` prefix. The Commands section's command list itself is unchanged (still run from the repo root, since the root scripts forward).

## Out of scope (deliberate, not an oversight)

- **`web/` is untouched.** Stays exactly as it is today (separate, non-workspace package) — moving it is sub-project 2's job. Keeping it out lets this sub-project be verified in isolation: `pnpm test`/`pnpm typecheck` must produce identical results before and after, with zero frontend involvement to confuse that comparison.
- **No dependency hoisting to the workspace root.** Even shared-sounding devDependencies (`typescript`, `prettier`) stay pinned inside `packages/core` for now — hoisting is easy to do later if real duplication pain shows up once `packages/platform-react` exists too; premature now with only one package.
- **No real microservice split.** This is a packaging move (one package → workspace-ready structure), not a deploy-topology change. `apps/<module>` for a second business module stays untriggered per `docs/architectures/04-strategy.md`, exactly as documented before this session started.
- **CI config.** No CI pipeline exists in this repo yet to update.

## Testing

This sub-project has an unusually strong correctness bar for a "refactor": since nothing about `packages/core`'s actual code changes (only its location and the wrapping `package.json`/workspace scaffolding), the test is that **every existing verification command, run from the repo root exactly as before, produces identical results**:
- `pnpm typecheck` — no errors, same as before the move.
- `pnpm test` — same 137 tests passing, same file count.
- `pnpm lint` — same output as before (baseline, including any pre-existing warnings).
- `pnpm dev` — API server starts and serves `/health` the same as before.

No new tests are written — there's no new *behavior* to test, only a location to verify is unchanged. If any of the above produces a different result than the pre-move baseline, that's a defect in the move itself, not a design gap to spec around.
