# FE E2E Test Suite Design

## Problem

`docs/superpowers/plans/2026-08-02-fe-manual-verification-plan.md` drove `apps/demo` through every existing flow with a throwaway Playwright script (deliberately not committed, per that plan's spec — "diagnostic tooling for this one pass, not new repo infrastructure") and produced `docs/bugs/2026-08-02.md`, 5 real findings plus a list of flows confirmed working. The user has now asked to keep and formalize that coverage rather than throw it away: a real `@playwright/test` suite, committed to the repo, with real `expect()` assertions — not the throwaway script's `console.log`-and-eyeball-the-screenshot approach.

This is the second sub-project of the FE/BE hardening effort (after the manual verification pass), decided in conversation:

1. FE manual verification pass (done — `docs/bugs/2026-08-02.md`).
2. **This: a real, committed FE E2E test suite covering the same ground** (this spec).
3. FE foundation (layout shell, auth UI, data grid, theming) — not yet spec'd.
4. BE foundational gaps — not yet spec'd.
5. Code/architecture quality audit — not yet spec'd.

## Decisions already made (recorded here so this spec doesn't re-litigate them)

- **Assertions encode desired behavior, not current behavior.** All 5 findings in `docs/bugs/2026-08-02.md` get a test asserting the *correct* outcome. Those tests fail red until the corresponding app bug is fixed — closing them is explicitly in scope for this sub-project, following this project's established TDD discipline (failing test → verified red → implementation → verified green), not a separate follow-up.
- **Each test creates and cleans up its own data**, no shared global fixture/seed step. Chosen after this session's manual pass hit exactly the failure mode this avoids: a shared `VERIFY-B` record got deleted by one flow (the viewer-role delete-attempt) partway through, breaking a later flow that expected it to still exist.
- **Playwright's `webServer` starts the backend and frontend dev servers**, not the developer running `pnpm dev`/`pnpm dev:web` by hand first (the alternative considered and rejected). Postgres/RabbitMQ (`docker compose up -d postgres rabbitmq`) are **not** auto-started — Playwright has no reasonable way to manage Docker Compose, and this matches how every other command in `docs/README.md`'s Commands section already assumes Docker is up.

## Scope

**In scope:**
- `@playwright/test` + config + `webServer` wiring in `apps/demo`.
- A JWT-minting test helper (no subprocess spawning per test).
- Spec files covering every flow from the manual pass's 11-flow scope (dev-login, entity list navigation, list sort/filter/infinite-scroll/virtualization, create, the reference-field picker, record detail including reference-field display, edit, a guarded workflow transition, delete, permission-aware UI under a restricted role, and both error states).
- Fixing all 5 findings from `docs/bugs/2026-08-02.md` so their corresponding tests go green.

**Out of scope:**
- CI wiring — this repo has no `.github/workflows` at all yet; adding one is a bigger, separate decision than this sub-project.
- The FE foundation and BE hardening sub-projects (layout shell, auth UI, data grid, etc.) — separate, not-yet-spec'd sub-projects.
- Any flow or component not already covered by the manual verification pass (e.g. nothing about a future data grid or nav shell — those don't exist yet).

## Architecture

Lives entirely inside `apps/demo` (the app actually being tested), not a new workspace package — there's no reuse need across apps yet, and `packages/platform-react`'s own components are only reachable through some hosting app's routes anyway.

```
apps/demo/
  playwright.config.ts       # testDir, webServer[], baseURL
  e2e/
    helpers/
      token.ts               # mintToken({ tenantId, userId }) -> JWT string
      api.ts                 # thin authenticated fetch wrapper for setup/teardown
      login.ts               # loginAs(page, token) -> completes the dev-login flow
    dev-login.spec.ts
    list.spec.ts
    record-crud.spec.ts
    workflow.spec.ts
    permission-aware-ui.spec.ts
    error-states.spec.ts
```

**`helpers/token.ts`** signs a JWT the same way `packages/core/scripts/mint-dev-token.mjs` does (`RS256`, `subject: userId`, `{ tenantId }` payload, 1h expiry), reading the private key from `packages/core/keys/dev-jwt-private.pem` resolved via `new URL("../../../../packages/core/keys/dev-jwt-private.pem", import.meta.url)` (robust to whatever directory the test runner's `cwd` happens to be, unlike a `process.cwd()`-relative path). Needs `jsonwebtoken@^9.0.3` (matching `packages/core`'s version) added to `apps/demo`'s `devDependencies` — signing in-process instead of spawning `pnpm mint-token` per test avoids child-process overhead across potentially dozens of tests.

**`helpers/api.ts`** wraps Playwright's `request` context (`APIRequestContext`) with the admin bearer token pre-attached, for the setup/teardown calls tests make directly against `http://localhost:3000` (create a record, grant a role, create a policy, and their inverses) — the same admin-gated endpoints (`/admin/users/:userId/roles`, `/admin/policies`, `/api/crm.customers`) the manual pass's script called via raw `fetch`/`curl`.

**`helpers/login.ts`** — `loginAs(page, token)` navigates to `/dev-login`, fills the textarea, clicks "Use token", waits for the redirect to `/`. Every spec's `beforeEach` calls this with a freshly-minted token; there is no shared logged-in state to reuse (the auth token lives only in React state — `packages/platform-react/src/auth/AuthContext.tsx` — not `localStorage`/cookies, so Playwright's `storageState` snapshot mechanism doesn't apply here, deliberately, per the existing "lost on refresh" design noted in `CLAUDE.md`). One consequence worth stating plainly: any in-test navigation *after* login must go through in-app clicks, not `page.goto()` — a hard navigation reloads the page and wipes the in-memory token, exactly the bug the manual pass's script hit and fixed (see that plan's Task 3 comments). Every spec file must navigate via clicking rendered links/buttons, never a bare `page.goto()` past the initial `/dev-login` visit.

## Test data isolation

Every spec's `beforeEach`/test body creates whatever records/roles/policies it needs through `helpers/api.ts` and tracks the returned ids; `afterEach` deletes exactly those ids, nothing else. This is safe under Playwright's default parallel test execution because:
- Records use a per-test-unique `code` (e.g. a random suffix), so two parallel tests never collide on the same row.
- A restricted-role test mints a **fresh random `userId`** per test and grants it the `viewer` role — `user_roles`' unique constraint is `(tenantId, userId, role)`, so many different userIds can all independently hold `viewer` with no conflict.
- A policy row (e.g. `email`/`read` restricted to `roles: ["admin"]`) is not a singleton — `policies` has no uniqueness constraint on `(entity, action, field)`, and `PermissionService.checkAction`/`PermissionSnapshot` OR-combine every matching row. Two parallel tests each creating their own copy of "the same" policy is harmless; each test deletes only the specific policy `id` its own setup call returned, never assuming it's the only one of that shape.

## Spec-to-finding mapping (what turns green when)

- `error-states.spec.ts` asserts a validation failure shows a human-readable message (not "Invalid input: expected string, received undefined") — closes finding 1 — and that a not-found record shows a styled error state with a way back to the list — closes finding 2.
- `record-crud.spec.ts`'s detail-view assertions wait for the reference field's resolved label before asserting on it (never asserting mid-load), and separately assert there's no persistent flash of a raw UUID once the fetch settles — closes finding 3. Its own back-to-list-link assertion, alongside `workflow.spec.ts`'s, closes finding 4 (both `RecordDetail` and `GeneratedForm` need a real link back to `/records/:entityName`).
- `permission-aware-ui.spec.ts` asserts a `viewer`-role user's delete attempt on a record is rejected (403), which requires `CreatePolicyBodySchema` (`packages/core/src/server/routes/admin.ts`) to accept a `"delete"` action so a delete-restricting policy can even be created — closes finding 5.

## Package/script wiring

- `apps/demo/package.json`: add `devDependencies`: `@playwright/test@1.62.1` (pinned to the exact version already verified working with the cached Chromium build in this session's sandbox, rather than a `^` range that could pull a newer version needing a fresh, potentially-blocked browser download) and `jsonwebtoken@^9.0.3`; add `"test:e2e": "playwright test"`.
- Root `package.json`: add `"test:e2e": "pnpm --filter @metap/demo test:e2e"`, matching every other root script's `--filter` forwarding pattern.
- `apps/demo/playwright.config.ts`: `testDir: "./e2e"`, `baseURL: "http://localhost:5173"`, `webServer: [{ command: "pnpm --filter @metap/crm dev", url: "http://localhost:3000/health", reuseExistingServer: !process.env.CI, cwd: "../.." }, { command: "pnpm --filter @metap/demo dev", url: "http://localhost:5173", reuseExistingServer: !process.env.CI, cwd: "../.." }]`.

## Testing (of the suite itself)

No meta-tests — the suite's own correctness is verified by running it: every spec file must be run once red (before its corresponding bug fix, for the 5 finding-closing assertions) and once green (after), matching this project's standard TDD verification discipline. Flows with no known bug (list browsing, create, workflow transition, admin-role delete) get tests that are expected to pass on the first run — if one doesn't, that's a new finding, not a suite bug, and gets its own entry appended to `docs/bugs/2026-08-02.md` before being fixed.

## File summary

- Create: `apps/demo/playwright.config.ts`
- Create: `apps/demo/e2e/helpers/token.ts`
- Create: `apps/demo/e2e/helpers/api.ts`
- Create: `apps/demo/e2e/helpers/login.ts`
- Create: `apps/demo/e2e/dev-login.spec.ts`
- Create: `apps/demo/e2e/list.spec.ts`
- Create: `apps/demo/e2e/record-crud.spec.ts`
- Create: `apps/demo/e2e/workflow.spec.ts`
- Create: `apps/demo/e2e/permission-aware-ui.spec.ts`
- Create: `apps/demo/e2e/error-states.spec.ts`
- Modify: `apps/demo/package.json` (devDependencies, `test:e2e` script)
- Modify: root `package.json` (`test:e2e` script)
- Modify (to close the 5 findings): `packages/platform-react/src/form/GeneratedForm.tsx`, `packages/platform-react/src/api/ApiErrorMessage.tsx`, `packages/platform-react/src/field/ReferenceFieldValue.tsx`, `packages/platform-react/src/detail/RecordDetail.tsx`, `packages/core/src/server/routes/admin.ts` (`CreatePolicyBodySchema`'s `action` enum), `packages/core/src/core/permission/permission-service.ts` (wherever `canDeleteEntity` needs to change to actually consult a `delete`-action policy — currently `checkAction` already takes an `EntityAction` including `"delete"`, so this may turn out to need only the schema change plus a route-level wiring check, not a `PermissionService` logic change; confirmed during implementation, not assumed here)
