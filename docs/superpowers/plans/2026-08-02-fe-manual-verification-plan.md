# FE Manual Verification Pass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drive the running `apps/demo` app through every flow `packages/platform-react` currently supports, using Playwright, and catalog everything broken or rough into `docs/bugs/2026-08-02.md`.

**Architecture:** A throwaway Playwright project in the scratchpad (never added to the repo) drives two browser sessions — one as an admin-role user (flows 1-9 and 11), one as a restricted-role user (flow 10) — capturing a screenshot and any console/network errors at each meaningful step. Nothing is fixed as part of this plan; findings are cataloged only, per `docs/superpowers/specs/2026-08-02-fe-manual-verification-design.md`.

**Tech Stack:** Playwright 1.62.1 (Node driver, not `@playwright/test`), the already-running `apps/crm` API (`:3000`) and `apps/demo` frontend (`:5173`), Postgres via `docker compose` (`:5433`).

## Global Constraints

- Backend (`pnpm dev`, port 3000), frontend (`pnpm dev:web`, port 5173), Postgres and RabbitMQ (`docker compose up -d postgres rabbitmq`, Postgres on host port 5433) are already running — confirmed this session (`curl localhost:3000/health` and `curl localhost:5173` both returned 200). Do not start new instances; if either is down, start it with the commands above before proceeding.
- Headless Chromium works in this sandbox as of this session (`sudo npx playwright install-deps chromium` was already run). If a fresh environment lacks this, `sudo npx playwright install-deps chromium` must be re-run first — that is a prerequisite of this plan, not one of its tasks.
- `SCRATCHPAD` = `/tmp/claude-1000/-home-minhtuan-dev-local-metap/5a4da9ba-a5fc-428c-bd23-2b583b7b6380/scratchpad` (this session's scratchpad directory). If executed in a different session, substitute that session's own scratchpad path from its system prompt — never write scratch files into the repo.
- All Playwright scripts and captured screenshots/logs live under `$SCRATCHPAD/fe-verify/` and are **never committed** — this is diagnostic tooling for this one pass, not new repo infrastructure (per the spec's explicit scope decision).
- `tenantId` `00000000-0000-0000-0000-000000000001` and `userId` `00000000-0000-0000-0000-000000000002` already exist in `user_roles` with role `admin` (confirmed via `docker exec metap-postgres-1 psql -U metap -d metap` this session) — reuse them for the admin session rather than minting a new admin.
- Only `docs/bugs/2026-08-02.md` (and the directory `docs/bugs/`, new) is a repo change from this plan. Every other file this plan creates lives in `$SCRATCHPAD` and must not be `git add`-ed.

---

### Task 1: Scratchpad Playwright project + capture helper

**Files:**
- Create: `$SCRATCHPAD/fe-verify/package.json`
- Create: `$SCRATCHPAD/fe-verify/lib/capture.mjs`

**Interfaces:**
- Produces: `attachCapture(page, artifactsDir, label) -> { shot(step: string): Promise<void>, logPath: string }` — every later task's scripts import this from `../lib/capture.mjs`.

- [ ] **Step 1: Create the scratchpad project and install Playwright**

```bash
mkdir -p "$SCRATCHPAD/fe-verify/lib" "$SCRATCHPAD/fe-verify/artifacts"
cd "$SCRATCHPAD/fe-verify"
npm init -y
npm install playwright@1.62.1
```

- [ ] **Step 2: Write the capture helper**

```js
// $SCRATCHPAD/fe-verify/lib/capture.mjs
import { mkdirSync, writeFileSync, appendFileSync } from "node:fs";
import { join } from "node:path";

export function attachCapture(page, artifactsDir, label) {
  mkdirSync(artifactsDir, { recursive: true });
  const logPath = join(artifactsDir, `${label}.log`);
  writeFileSync(logPath, "");

  page.on("console", (msg) => {
    if (msg.type() === "error" || msg.type() === "warning") {
      appendFileSync(logPath, `[console.${msg.type()}] ${msg.text()}\n`);
    }
  });
  page.on("pageerror", (err) => {
    appendFileSync(logPath, `[pageerror] ${err.message}\n`);
  });
  page.on("response", (response) => {
    if (response.status() >= 400) {
      appendFileSync(
        logPath,
        `[http ${response.status()}] ${response.request().method()} ${response.url()}\n`,
      );
    }
  });

  return {
    async shot(step) {
      await page.screenshot({ path: join(artifactsDir, `${label}-${step}.png`), fullPage: true });
    },
    logPath,
  };
}
```

- [ ] **Step 3: Verify Playwright launches in this project**

```bash
cd "$SCRATCHPAD/fe-verify"
node -e "
const { chromium } = require('playwright');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto('data:text/html,<h1>ok</h1>');
  console.log('LAUNCH OK', await page.title());
  await browser.close();
})();
"
```

Expected: prints `LAUNCH OK ok`. If it fails with a missing shared library error, re-run `sudo npx playwright install-deps chromium` (outside the scope of this plan's tasks — a session prerequisite) before continuing.

- [ ] **Step 4: Commit check**

Nothing to commit — everything in this task lives under `$SCRATCHPAD`, which is outside the repo.

---

### Task 2: Mint tokens and seed a restricted test role + policies

**Files:** none (backend admin API calls and repo's existing `pnpm mint-token` script only — no new files).

**Interfaces:**
- Produces: two JWTs (`ADMIN_TOKEN`, `VIEWER_TOKEN`, printed to the terminal and reused as shell env vars in Tasks 3-4), and two policy rows in the `policies` table that Task 4's script depends on (a `read` mask on `crm.customers.email`, a `write` restriction on `crm.customers.phone`, both scoped to `roles: ["admin"]` — i.e. denied to anyone without the `admin` role).

- [ ] **Step 1: Mint the admin token (existing seeded admin user)**

```bash
cd /home/minhtuan/dev/local/metap
pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002
```

Copy the printed JWT; export it for later steps:

```bash
export ADMIN_TOKEN="<paste the token printed above>"
```

- [ ] **Step 2: Mint a token for a second, not-yet-privileged user**

```bash
pnpm mint-token 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000003
export VIEWER_TOKEN="<paste the token printed above>"
```

This user has no rows in `user_roles` yet, so this token currently carries no roles at all (`RequestContext.roles` resolves empty) — Step 3 below grants it `viewer`.

- [ ] **Step 3: Grant the `viewer` role to the second user via the admin API**

```bash
curl -s -X POST http://localhost:3000/admin/users/00000000-0000-0000-0000-000000000003/roles \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"role":"viewer"}'
```

Expected: HTTP 201, body `{"data":["viewer"]}`.

- [ ] **Step 4: Create the field-level read-mask policy on `email`**

```bash
curl -s -X POST http://localhost:3000/admin/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","action":"read","field":"email","roles":["admin"]}'
```

Expected: HTTP 201, body's `data.field` is `"email"`. This means any non-admin role (including `viewer`) has `email` stripped from every record it reads.

- [ ] **Step 5: Create the field-level write-restriction policy on `phone`**

```bash
curl -s -X POST http://localhost:3000/admin/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","action":"write","field":"phone","roles":["admin"]}'
```

Expected: HTTP 201, body's `data.field` is `"phone"`. This means `viewer` can read `phone` but not write it — `GeneratedForm` should render its input disabled with the description "You can't edit this field" for that role.

- [ ] **Step 6: Verify both policies are visible**

```bash
curl -s "http://localhost:3000/admin/policies?entity=crm.customers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -m json.tool
```

Expected: an array of 2 policy objects (`email`/`read`, `phone`/`write`), both with `"roles":["admin"]`.

**Note for the findings doc (Task 5):** `CreatePolicyBodySchema` (`packages/core/src/server/routes/admin.ts`) only accepts `action: "read" | "create" | "update" | "write"` — there is no way to create a `delete`-action policy through this API even though `PermissionService.canDeleteEntity` checks for one. This means Task 4's viewer session will be able to delete records exactly like an admin. That's a real backend gap, out of scope to fix here, but must be recorded as a finding, not silently worked around.

---

### Task 3: Write and run the admin-session script (flows 1–9, 11)

**Files:**
- Create: `$SCRATCHPAD/fe-verify/admin-flow.mjs`

**Interfaces:**
- Consumes: `attachCapture` from Task 1 (`../lib/capture.mjs` relative to this file, i.e. `./lib/capture.mjs` from `$SCRATCHPAD/fe-verify`), `ADMIN_TOKEN` env var from Task 2.
- Produces: `$SCRATCHPAD/fe-verify/artifacts/admin-*.png` and `admin.log`, read back in Task 5.

- [ ] **Step 1: Write the script**

```js
// $SCRATCHPAD/fe-verify/admin-flow.mjs
import { chromium } from "playwright";
import { attachCapture } from "./lib/capture.mjs";

const ADMIN_TOKEN = process.env.ADMIN_TOKEN;
if (!ADMIN_TOKEN) {
  throw new Error("Set ADMIN_TOKEN before running this script.");
}

const BASE = "http://localhost:5173";
const API = "http://localhost:3000";
const ARTIFACTS = new URL("./artifacts", import.meta.url).pathname;

async function seedBulkRecords() {
  // GeneratedList's listView.maxLimit for crm.customers is 100, so >100 rows
  // are needed to actually exercise infinite-scroll/keyset pagination.
  for (let i = 0; i < 120; i++) {
    const res = await fetch(`${API}/api/crm.customers`, {
      method: "POST",
      headers: { Authorization: `Bearer ${ADMIN_TOKEN}`, "Content-Type": "application/json" },
      body: JSON.stringify({
        data: { code: `BULK-${String(i).padStart(4, "0")}`, name: `Bulk Customer ${i}` },
      }),
    });
    if (!res.ok) {
      throw new Error(`Seeding bulk record ${i} failed: ${res.status} ${await res.text()}`);
    }
  }
  console.log("Seeded 120 bulk records.");
}

async function clientNavigate(page, path) {
  // page.goto() is a hard navigation (like F5). The auth token lives only in
  // React state (deliberately, per CLAUDE.md — "lost on refresh, not a
  // bug"), so a hard nav wipes it and RequireAuth bounces to /dev-login.
  // React Router's BrowserRouter listens for popstate, so a manual
  // pushState+popstate is a client-side nav instead, same as a real user
  // clicking a link — use this for any navigation after the initial login.
  await page.evaluate((p) => {
    window.history.pushState({}, "", p);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await page.waitForTimeout(200);
}

async function run() {
  await seedBulkRecords();

  const browser = await chromium.launch();
  const page = await browser.newPage();
  const cap = attachCapture(page, ARTIFACTS, "admin");
  page.on("dialog", (dialog) => dialog.accept());

  // --- Flow 1: dev-login ---
  await page.goto(`${BASE}/dev-login`);
  await page.getByLabel(/Paste a JWT/).fill(ADMIN_TOKEN);
  await cap.shot("01-dev-login-filled");
  await page.getByRole("button", { name: "Use token" }).click();
  await page.waitForURL(`${BASE}/`);
  await cap.shot("02-entities-page");

  // --- Flow 2: EntitiesPage -> select crm.customers ---
  await page.getByRole("link", { name: /Customer \(crm\.customers\)/ }).click();
  await page.waitForURL(`${BASE}/records/crm.customers`);
  await cap.shot("03-list-initial");

  // --- Flow 3: GeneratedList — sort, filter, infinite scroll ---
  const nameHeader = page.locator("table thead tr").first().locator("th").nth(1);
  await nameHeader.click();
  await cap.shot("04-sort-asc");
  await nameHeader.click();
  await cap.shot("05-sort-desc");

  const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
  await nameFilter.fill("Bulk Customer 1");
  await page.waitForTimeout(600); // debounce (400ms) + refetch
  await cap.shot("06-name-filtered");
  await nameFilter.fill("");
  await page.waitForTimeout(600);

  const scrollContainer = page.locator('div[style*="overflow: auto"]').first();
  const rowCountBeforeScroll = await page.locator("table tbody tr").count();
  await scrollContainer.evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await page.waitForTimeout(1000);
  const rowCountAfterScroll = await page.locator("table tbody tr").count();
  await cap.shot("07-after-scroll");
  console.log(`Rows before scroll: ${rowCountBeforeScroll}, after: ${rowCountAfterScroll}`);

  // --- Flow 4 + 8: create two records, second one referencing the first ---
  // Note: "New" renders as <Button component={navAdapter.Link}>, i.e. an <a>
  // (role "link"), not a real <button> — accessible role is link, not button.
  await page.getByRole("link", { name: "New" }).click();
  await page.waitForURL(`${BASE}/records/crm.customers/new`);
  await page.getByLabel("Code *").fill("VERIFY-A");
  await page.getByLabel("Name *").fill("Verify Customer A");
  await page.getByLabel("Phone").fill("555-0100");
  await page.getByLabel("Email").fill("verify-a@example.com");
  await cap.shot("08-create-a-filled");
  await page.getByRole("button", { name: "Save" }).click();
  await page.waitForURL(`${BASE}/records/crm.customers`);
  await cap.shot("09-after-create-a");

  await page.getByRole("link", { name: "New" }).click();
  await page.waitForURL(`${BASE}/records/crm.customers/new`);
  await page.getByLabel("Code *").fill("VERIFY-B");
  await page.getByLabel("Name *").fill("Verify Customer B");
  await page.getByLabel(/Email/).fill("verify-b@example.com");
  const referredByInput = page.getByRole("combobox", { name: "Referred By" });
  await referredByInput.click();
  await referredByInput.fill("Verify Customer A");
  await page.waitForTimeout(500); // search debounce (300ms)
  await cap.shot("10-reference-picker-open");
  await page.getByRole("option", { name: "Verify Customer A" }).click();
  await cap.shot("11-reference-picker-selected");
  await page.getByRole("button", { name: "Save" }).click();
  await page.waitForURL(`${BASE}/records/crm.customers`);
  await cap.shot("12-after-create-b");

  // --- Flow 5: RecordDetail — view record B, confirm reference display ---
  const rowB = page.locator("table tbody tr", { hasText: "VERIFY-B" });
  await rowB.getByRole("link", { name: "View" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
  await page.getByText("Referred By", { exact: true }).waitFor({ state: "visible" });
  await cap.shot("13-detail-b");
  const detailBodyText = await page.locator("body").innerText();
  console.log(`Detail page body text (record B):\n${detailBodyText}`);

  // --- Flow 6: edit record B ---
  await page.getByRole("link", { name: "Edit" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/[^/]+\/edit$/);
  await page.getByLabel("Phone").fill("555-0200");
  await cap.shot("14-edit-b-filled");
  await page.getByRole("button", { name: "Save" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
  await cap.shot("15-after-edit-b");

  // --- Flow 7: workflow transition ---
  await cap.shot("16-workflow-before-transition");
  const activateButton = page.getByRole("button", { name: /^Activate/ });
  const activateDisabled = await activateButton.isDisabled();
  console.log(`Activate button disabled before transition: ${activateDisabled}`);
  await activateButton.click();
  await page.waitForTimeout(500);
  await cap.shot("17-workflow-after-transition");

  // --- Flow 9: delete (one of the bulk-seeded records, not A or B) ---
  await clientNavigate(page, "/records/crm.customers");
  const nameFilter2 = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
  await nameFilter2.fill("Bulk Customer 0");
  await page.waitForTimeout(600);
  await cap.shot("18-before-delete");
  await page.locator("table tbody tr").first().getByRole("button", { name: "Delete" }).click();
  await page.waitForTimeout(500);
  await cap.shot("19-after-delete");

  // --- Flow 11: error states ---
  await clientNavigate(page, "/records/crm.customers/new");
  await page.getByRole("button", { name: "Save" }).click();
  await page.waitForTimeout(300);
  await cap.shot("20-validation-error");

  await clientNavigate(page, "/records/crm.customers/00000000-0000-0000-0000-000000000000");
  await page.waitForTimeout(500);
  await cap.shot("21-not-found-error");

  await browser.close();
  console.log(`Admin flow complete. Artifacts in ${ARTIFACTS}, log at ${cap.logPath}`);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
```

- [ ] **Step 2: Run it**

```bash
cd "$SCRATCHPAD/fe-verify"
ADMIN_TOKEN="$ADMIN_TOKEN" node admin-flow.mjs
```

Expected: the script runs to completion and prints `Admin flow complete. Artifacts in ...`. It is normal and expected for this run to surface bugs (that's the point) — a thrown error, a screenshot showing something wrong, or lines in `admin.log` are all findings for Task 5, not failures of this task. This task's own completion criterion is narrower: the script ran end-to-end without an *unhandled* exception (an unhandled exception means the script itself is broken — e.g. a wrong selector — and needs a fix before it can produce a usable set of artifacts; a *handled*, logged problem in the app under test is a finding, not a script bug).

- [ ] **Step 3: If the script throws partway through**

Read the stack trace — a `TimeoutError` on a `getByLabel`/`getByRole` locator usually means either the app genuinely doesn't render what the flow expects (a real finding — screenshot the state manually via `await page.screenshot(...)` inserted right before the failing line, note it, and adjust the script to skip past that step so the rest of the flow can still run) or the selector text doesn't match the actual rendered label/button text (a script bug — fix the selector to match and re-run from the top, since `seedBulkRecords` is idempotent-enough for a re-run to just add more bulk rows, which doesn't invalidate later flow steps).

- [ ] **Step 4: Commit check**

Nothing to commit — `admin-flow.mjs` and its artifacts live under `$SCRATCHPAD`.

---

### Task 4: Write and run the viewer-session script (flow 10)

**Files:**
- Create: `$SCRATCHPAD/fe-verify/viewer-flow.mjs`

**Interfaces:**
- Consumes: `attachCapture` from Task 1, `VIEWER_TOKEN` from Task 2, the `VERIFY-B` record created in Task 3 (this task must run after Task 3).
- Produces: `$SCRATCHPAD/fe-verify/artifacts/viewer-*.png` and `viewer.log`, read back in Task 5.

- [ ] **Step 1: Write the script**

```js
// $SCRATCHPAD/fe-verify/viewer-flow.mjs
import { chromium } from "playwright";
import { attachCapture } from "./lib/capture.mjs";

const VIEWER_TOKEN = process.env.VIEWER_TOKEN;
if (!VIEWER_TOKEN) {
  throw new Error("Set VIEWER_TOKEN before running this script.");
}

const BASE = "http://localhost:5173";
const ARTIFACTS = new URL("./artifacts", import.meta.url).pathname;

async function clientNavigate(page, path) {
  // page.goto() is a hard navigation (like F5) and wipes the in-memory-only
  // auth token (see Task 3's admin-flow.mjs comment for the full reasoning).
  // Use client-side history nav for anything after the initial login.
  await page.evaluate((p) => {
    window.history.pushState({}, "", p);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await page.waitForTimeout(200);
}

async function run() {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  const cap = attachCapture(page, ARTIFACTS, "viewer");
  page.on("dialog", (dialog) => dialog.accept());

  await page.goto(`${BASE}/dev-login`);
  await page.getByLabel(/Paste a JWT/).fill(VIEWER_TOKEN);
  await page.getByRole("button", { name: "Use token" }).click();
  await page.waitForURL(`${BASE}/`);

  await page.getByRole("link", { name: /Customer \(crm\.customers\)/ }).click();
  await page.waitForURL(`${BASE}/records/crm.customers`);

  const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
  await nameFilter.fill("Verify Customer B");
  await page.waitForTimeout(600);
  await cap.shot("01-list-filtered-to-b");

  const headerTexts = await page.locator("table thead tr").first().locator("th").allTextContents();
  console.log(`List column headers as viewer: ${JSON.stringify(headerTexts)}`);
  const emailCellText = await page.locator("table tbody tr").first().locator("td").nth(3).textContent();
  console.log(`Email column value as viewer (should be masked/empty, not "verify-b@example.com"): "${emailCellText}"`);

  await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
  await cap.shot("02-detail-b-as-viewer");

  await page.getByRole("link", { name: "Edit" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/[^/]+\/edit$/);
  await cap.shot("03-edit-b-as-viewer");
  const phoneInput = page.getByLabel("Phone");
  const phoneDisabled = await phoneInput.isDisabled();
  console.log(`Phone input disabled for viewer (should be true — write policy restricts it to admin): ${phoneDisabled}`);
  const nameInput = page.getByLabel("Name *");
  const nameDisabled = await nameInput.isDisabled();
  console.log(`Name input disabled for viewer (should be false — no write policy on name): ${nameDisabled}`);

  await clientNavigate(page, "/records/crm.customers");
  await nameFilter.fill("Verify Customer B");
  await page.waitForTimeout(600);
  await cap.shot("04-before-delete-attempt");
  await page.locator("table tbody tr").first().getByRole("button", { name: "Delete" }).click();
  await page.waitForTimeout(500);
  await cap.shot("05-after-delete-attempt");

  await browser.close();
  console.log(`Viewer flow complete. Artifacts in ${ARTIFACTS}, log at ${cap.logPath}`);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
```

- [ ] **Step 2: Run it**

```bash
cd "$SCRATCHPAD/fe-verify"
VIEWER_TOKEN="$VIEWER_TOKEN" node viewer-flow.mjs
```

Expected: same completion criterion as Task 3 Step 2 — runs to completion without an unhandled exception; console lines about masked email / disabled phone / delete outcome are read in Task 5, not judged here.

- [ ] **Step 3: If the script throws partway through**

Same handling as Task 3 Step 3.

- [ ] **Step 4: Commit check**

Nothing to commit — lives under `$SCRATCHPAD`.

---

### Task 5: Compile findings into `docs/bugs/2026-08-02.md` and commit

**Files:**
- Create: `docs/bugs/2026-08-02.md`

**Interfaces:**
- Consumes: every `$SCRATCHPAD/fe-verify/artifacts/admin-*.png`, `admin.log`, `viewer-*.png`, `viewer.log`, and both scripts' captured `console.log` output from Tasks 3-4.

- [ ] **Step 1: Read every screenshot and both log files**

Go through `admin-*.png`/`viewer-*.png` in step order alongside `admin.log`/`viewer.log`, comparing each screenshot against what Task 3/4's script comments say that step was supposed to show (e.g. step `17-workflow-after-transition` should show the badge on "active", not "draft"; step `06-name-filtered` should show only rows containing "Bulk Customer 1"; the viewer's `emailCellText` console line should not equal `"verify-b@example.com"`).

- [ ] **Step 2: Write `docs/bugs/2026-08-02.md`**

```bash
mkdir -p /home/minhtuan/dev/local/metap/docs/bugs
```

Use this structure (fill in real entries from Step 1's findings — do not leave this as a template, every finding gets a real, specific entry):

```markdown
# FE Verification Findings — 2026-08-02

Findings from the Playwright-driven verification pass described in
`docs/superpowers/specs/2026-08-02-fe-manual-verification-design.md` and
`docs/superpowers/plans/2026-08-02-fe-manual-verification-plan.md`. Entries
are in the order found, not pre-sorted by severity or priority — priority is
set by review of this list, not by this document.

### <short title>

- **Flow:** <one of the 11 flows from the design spec>
- **Severity:** blocker / major / minor / polish
- **Repro:** <exact steps>
- **Expected vs actual:** <what should have happened vs what the screenshot/log showed>
- **Likely location:** <file/component, if apparent from the symptom>

<repeat one entry per real finding>

## Known gaps found during setup (not from a UI flow)

- `CreatePolicyBodySchema` (`packages/core/src/server/routes/admin.ts`) has no
  `"delete"` action — a `delete`-action policy cannot be created through the
  admin API even though `PermissionService.canDeleteEntity` checks for one.
  Confirmed by observation: the `viewer`-role session in this pass could
  delete `crm.customers` records exactly like the admin session, because no
  restricting policy could be created for it.
```

If the pass finds zero problems in a given flow, do not fabricate a finding — omit that flow from the list. It is fine for this document to end up shorter than the 11-flow scope if the app genuinely holds up; that is itself the useful signal.

- [ ] **Step 3: Commit**

```bash
cd /home/minhtuan/dev/local/metap
git add docs/bugs/2026-08-02.md
git status
```

Confirm `git status` shows only `docs/bugs/2026-08-02.md` staged — nothing from `$SCRATCHPAD` should ever appear here, since it was never written inside the repo. Then:

```bash
git commit -m "$(cat <<'EOF'
Add FE verification findings from Playwright-driven manual pass

Catalogs real, browser-verified bugs across packages/platform-react
and apps/demo (sub-project 1 of the FE/BE hardening effort). No
fixes yet — fix order is set by user review of this list next.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01TNpgCXp7ihGYoKmoWPmJNk
EOF
)"
```

- [ ] **Step 4: Present the list to the user**

Summarize the findings count and severities in chat, and ask which to fix first — per the design spec's agreed process (catalog everything, user sets fix priority, then fix one at a time with normal TDD discipline). Do not start fixing anything in this task.
