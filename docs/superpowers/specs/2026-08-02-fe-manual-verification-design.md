# FE Manual Verification Pass — Design

## Problem

Phase 6 (Frontend Core) in `docs/roadmap.md` is marked "Done," but every sub-project's status note carries the same caveat: passed typecheck/build/lint/the backend test suite, **never browser-verified** — the sandbox had no working headless Chromium (missing system libraries, no `sudo`). That's no longer true: `sudo npx playwright install-deps chromium` was run this session, and a headless launch now succeeds. This is sub-project 1 of the broader FE/BE hardening effort the user scoped as "1 → 4" (FE verification → FE foundation → BE foundational gaps → code/architecture audit; see conversation), and the first opportunity to actually exercise `packages/platform-react` + `apps/demo` end-to-end and find out what's really rough before building anything new on top of it.

## Scope

**In scope:** driving the existing `apps/demo` app (already running: API on `:3000`, frontend on `:5173`, Postgres/RabbitMQ up) through every flow `packages/platform-react`'s components currently support, cataloging what's broken or rough. Flows:

1. Dev-login (`/dev-login` → paste a minted token → lands on entities page).
2. `EntitiesPage` → select an entity.
3. `GeneratedList`: sort, filter, search, infinite-scroll pagination, row virtualization.
4. Create (list's "New" button → `GeneratedForm` submit).
5. `RecordDetail` (field display, including the newly-added reference field display).
6. Edit an existing record.
7. `WorkflowActionBar` (available transitions, guard-disabled buttons).
8. `ReferenceFieldInput` (search-autocomplete picker for `referredBy`).
9. Delete.
10. Permission-aware UI (masked/disabled fields and buttons under a non-admin role — requires minting a second token with a restricted role).
11. Error states (validation failure, network/API error surfaced via `ApiErrorMessage`).

**Out of scope:** fixing anything found (that's a follow-up pass per bug, prioritized after cataloging — already agreed), any new component or FE foundation work (sub-project 2), backend feature gaps not surfaced through the UI (sub-project 3), and setting up a permanent/CI-integrated E2E suite — this is a one-off diagnostic pass, not new committed test infrastructure. If the pass reveals real value in keeping it as ongoing coverage, that's a decision for later, not assumed here.

## Approach

Hybrid: a Playwright script drives each flow above, capturing a screenshot and any console/network errors at each meaningful step, writing artifacts to the session scratchpad (not committed — this is diagnostic tooling, not new repo infrastructure). After each flow runs, the screenshots/logs are read back and any flow with an unexpected result gets a short follow-up round of interactive Playwright commands to narrow down exactly what's wrong before writing it up.

Chosen over a fully scripted pass (fast and repeatable, but can't adapt to something unexpected) or fully interactive driving (adaptive, but far too slow for 11 flows). The script is kept only for the duration of this pass, to re-run against a fix and confirm it before moving to the next bug — not registered as a permanent `pnpm` command or CI step.

Two tokens are needed up front: an admin token (`pnpm mint-token`) for flows 1-9, and one with a restricted role for flow 10 — minted the same way with a non-admin role once a suitable policy exists for `crm.customers` (falls back to noting the gap in the findings doc if no restrictive policy is currently seeded).

## Output

`docs/bugs/2026-08-02.md` (new file, new `docs/bugs/` directory — one dated file per verification pass, per user's stated preference). Each entry:

```markdown
### <short title>

- **Flow:** which of the 11 flows above
- **Severity:** blocker / major / minor / polish
- **Repro:** exact steps
- **Expected vs actual:**
- **Likely location:** file/component, if apparent from the symptom
```

Entries are appended in the order found, not pre-sorted — after the full pass, the user reviews the list and sets fix priority (already agreed: catalog everything first, then fix in the order the user picks).

## Process

1. Mint the admin token, confirm `apps/demo` loads and dev-login works.
2. Run the script through flows 1-9 sequentially against the admin token.
3. Attempt flow 10 (permission-aware UI); note in the findings doc if no restrictive policy exists to test against rather than fabricating one out of scope.
4. Run flow 11's error-state checks (invalid form input, a deliberately-bad request).
5. Compile `docs/bugs/2026-08-02.md` from everything captured.
6. Present the list to the user for prioritization before any fix begins.

## Testing

Not applicable in the usual TDD sense — this sub-project produces a findings document, not code. Fixes made in response to individual findings (the follow-up work this pass feeds into) follow the project's normal TDD discipline per finding, scoped and reviewed one at a time once the user sets priority.
