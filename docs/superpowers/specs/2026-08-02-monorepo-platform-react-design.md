# Monorepo Restructure, Sub-project 2: `packages/platform-react` + `apps/demo`

Date: 2026-08-02

Status: approved

Scope: second of the 3-part monorepo restructure (see `docs/superpowers/specs/2026-08-02-monorepo-packages-core-design.md` for sub-project 1, which established the workspace and `packages/core`). Third part (real multi-service backend split) stays deferred, untriggered.

## Motivation

`web/` is today a separate, non-workspace package with its own lockfile, split internally into `platform/` (reusable pieces) and `demo/` (throwaway pages), per `docs/architectures/04-strategy.md`'s "Future Evolution: Frontend Platform Package" — which explicitly named the trigger for extracting `platform/` into its own installable package as "an actual second app needing to import it." This sub-project pulls that trigger: `platform/` becomes a real workspace package (`packages/platform-react`), and `web/` (renamed `apps/demo`) becomes its first real consumer via `workspace:*`, proving the extraction actually works rather than just reorganizing folders within one app.

## Design

### `packages/platform-react`

Package name `@metap/platform-react`, `private: true` (not published to any registry — this is workspace-internal packaging, not a publishing decision; publishing is a separate, later trigger per the strategy doc).

Content: today's `web/src/platform/*` (`api/`, `auth/`, `detail/`, `field/`, `form/`, `list/`, `metadata/`, `workflow/`) moves as-is into `packages/platform-react/src/`. New `src/index.ts` barrel, re-exporting every currently-exported symbol from each module (components, hooks, types) — the package's public surface.

No build step: `package.json`'s `main`/`types` point directly at `./src/index.ts`. Vite (in `apps/demo`) consumes the TypeScript source directly through the `workspace:*` link — consistent with `apps/demo`'s own `tsconfig.app.json` already having `noEmit: true` (nothing in this app is ever compiled by `tsc`, only bundled by Vite). Adding a compile step here would be new complexity solving a problem that doesn't exist yet.

**Dependency split** (standard practice for a shared UI library, worth doing now while it's a one-line categorization rather than a later migration):
- `peerDependencies`: `react`, `react-dom`, `react-router-dom`, `@tanstack/react-query`, `@mantine/core`, `@mantine/hooks` — every one of these carries app-wide singleton state (React's own reconciler, React Query's `QueryClient` cache, Mantine's theme/portal context, the router's history). Two independent copies of any of them in one component tree causes real, hard-to-debug bugs (duplicate contexts, hooks that silently no-op). A consumer must provide exactly one copy; `apps/demo` does, today.
- `dependencies`: `@mantine/dates`, `@mantine/notifications`, `dayjs` — plain component/utility libraries, no singleton-context risk.

**Known limitation, not fixed here:** the package has a hard dependency on `react-router-dom` (`WorkflowActionBar`, `RecordDetail`, and `GeneratedForm`'s navigation all use `Link`/`useNavigate`). This is a real gap against `04-strategy.md`'s stated "stays agnostic about how a consumer is built" goal — a consumer using a different router (or no router) can't use these three components as-is. Decoupling navigation from the package (e.g. accepting navigation as injected callbacks/render props) is a separate, larger redesign; recorded here as a known limitation, not undertaken as part of this packaging move.

### `apps/demo`

`git mv web apps/demo`. Package renamed `@metap/demo`. `src/platform/` is deleted (moved out, not duplicated); `src/demo/`, `src/App.tsx`, `src/main.tsx`, `src/index.css`, `src/assets/`, `index.html`, `vite.config.ts`, `tsconfig*.json`, `postcss.config.cjs`, `.oxlintrc.json` all stay, unchanged in content except import paths.

New dependency: `"@metap/platform-react": "workspace:*"`.

Import rewrites — the only functional code change in this sub-project, mechanical: 7 import statements across 4 files (`App.tsx`, `main.tsx`, `src/demo/DevLoginPage.tsx`, `src/demo/EntitiesPage.tsx`) currently reading `from "./platform/..."` / `from "../platform/..."` become `from "@metap/platform-react"`.

`web/pnpm-lock.yaml` (the separate lockfile `web/` has had since it was never a workspace member) is deleted; the single root `pnpm-lock.yaml` becomes authoritative for the whole workspace, same as `packages/core` already is.

### Root scripts

Quality-check scripts become workspace-recursive, since there's now genuinely more than one package to check: `typecheck`, `test`, `lint`, `format`, `format:check`, `build` all change from `pnpm --filter @metap/core <script>` to `pnpm -r <script>` — pnpm skips any package that doesn't define a given script, so this is safe even though not every package defines every script identically.

Operational, inherently single-target scripts stay `@metap/core`-only, unchanged from sub-project 1: `dev`, `start`, `worker:outbox`, `index:reconcile`, `db:generate`, `db:migrate`, `db:migrate:test`, `db:studio`, `auth:dev-keys`, `mint-token`, `seed:admin`.

New script: `dev:web` → `pnpm --filter @metap/demo dev` (frontend dev server, port 5173, same as today's `cd web && pnpm dev`).

## Out of scope (deliberate, not an oversight)

- **Publishing `@metap/platform-react` to any registry.** Still workspace-internal; publishing is `04-strategy.md`'s own next-named trigger, separate from this one.
- **Decoupling `react-router-dom` from the package** (see Known Limitation above). Real gap, real future work, not this sub-project.
- **Running backend + frontend dev servers concurrently from one root command.** `pnpm dev` (backend) and `pnpm dev:web` (frontend) stay two separate commands in two terminals, matching today's actual workflow — no `concurrently`/`npm-run-all` dependency added to solve a problem nobody's hit yet.
- **`apps/<second business module>` / real multi-service backend split.** Still untriggered (sub-project 3, deferred), unaffected by this frontend-focused sub-project.

## Testing

Same bar as sub-project 1 — nothing about the *behavior* of any component changes, so the test is that everything still works identically after the move:
- `pnpm typecheck` (now `pnpm -r typecheck`) — no errors in either package.
- `pnpm test` (now `pnpm -r test`) — same 137 backend tests pass; `apps/demo` has no test framework (unchanged from today's `web/`).
- `pnpm lint` (now `pnpm -r lint`) — same clean baseline in both packages.
- `pnpm format:check` (now `pnpm -r format:check`) — clean in both.
- `pnpm --filter @metap/demo build` — production build succeeds, importing `@metap/platform-react` through the workspace link.
- Manual browser check: `pnpm dev` + `pnpm dev:web`, confirm the app still logs in, lists, creates, edits, and deletes a `crm.customers` record exactly as before — this sandbox has had no working headless browser all session (missing system libraries, no `sudo`, no cached alternative); if still true, that gets reported plainly rather than claimed as verified.
