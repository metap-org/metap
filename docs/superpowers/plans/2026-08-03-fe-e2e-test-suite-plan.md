# FE E2E Test Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a real, committed `@playwright/test` suite in `apps/demo` covering every flow from the manual verification pass, and close all 5 findings in `docs/bugs/2026-08-02.md` by writing a failing test for each (asserting the desired behavior), then fixing the app code to make it pass.

**Architecture:** Test files live in `apps/demo/e2e/`, driven by 3 small helpers (`mintToken`, `loginAs`/`goToCustomersList`, and an authenticated API client for setup/teardown). Every test creates and deletes its own data through the backend's real HTTP API — no shared seed step, no test ordering dependencies. `playwright.config.ts`'s `webServer` array starts the backend and frontend dev servers itself; Postgres/RabbitMQ must already be running via `docker compose up -d postgres rabbitmq` (unchanged from every other command in this repo).

**Tech Stack:** `@playwright/test@1.62.1` (pinned — matches the Chromium build already verified working in this session's sandbox), `jsonwebtoken@^9.0.3` (matches `packages/core`'s version).

## Global Constraints

- Postgres/RabbitMQ must be running (`docker compose up -d postgres rabbitmq`, repo root) before `pnpm test:e2e` — `webServer` starts the app servers, not Docker.
- The seeded admin user must already exist: `pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002` (one-time, same as this project's existing documented setup step for `auth:dev-keys`).
- `packages/core/keys/dev-jwt-private.pem` must exist (`pnpm auth:dev-keys`, one-time) — the token helper reads it directly.
- Every test creates its own data via the API and deletes exactly what it created in `afterEach` — never assume a test is the only thing touching `crm.customers`/`policies`/`user_roles`.
- Every spec file must reach any post-login page via clicking rendered links/buttons (or `page.goBack()`), never a bare `page.goto()` after the initial `/dev-login` visit — a hard navigation wipes the in-memory-only auth token (`packages/platform-react/src/auth/AuthContext.tsx`).
- Tenant id `00000000-0000-0000-0000-000000000001` and admin user id `00000000-0000-0000-0000-000000000002` are fixed constants used throughout (already seeded with the `admin` role in this project's dev DB).

---

### Task 1: Playwright infra + `dev-login.spec.ts` (smoke test)

**Files:**
- Create: `apps/demo/playwright.config.ts`
- Create: `apps/demo/e2e/helpers/token.ts`
- Create: `apps/demo/e2e/helpers/login.ts`
- Create: `apps/demo/e2e/dev-login.spec.ts`
- Modify: `apps/demo/package.json` (add `devDependencies`, add `test:e2e` script)
- Modify: root `package.json` (add `test:e2e` script)

**Interfaces:**
- Produces: `mintToken(tenantId: string, userId: string): string` (`helpers/token.ts`) — used by every later spec file.
- Produces: `loginAs(page: Page, token: string): Promise<void>` and `goToCustomersList(page: Page): Promise<void>` (`helpers/login.ts`) — used by every later spec file.

- [ ] **Step 1: Add devDependencies and the `test:e2e` script**

In `apps/demo/package.json`, add to `"devDependencies"` (alongside the existing entries):

```json
    "@playwright/test": "1.62.1",
    "jsonwebtoken": "^9.0.3",
    "@types/jsonwebtoken": "^9.0.7",
```

And add to `"scripts"`:

```json
    "test:e2e": "playwright test",
```

Then in the repo root `package.json`, add to `"scripts"` (alongside `"test": "pnpm -r test"`):

```json
    "test:e2e": "pnpm --filter @metap/demo test:e2e",
```

Run `pnpm install` from the repo root afterward.

- [ ] **Step 2: Write the JWT-minting helper**

```ts
// apps/demo/e2e/helpers/token.ts
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import jwt from "jsonwebtoken";

const PRIVATE_KEY_PATH = fileURLToPath(
  new URL("../../../../packages/core/keys/dev-jwt-private.pem", import.meta.url),
);

export function mintToken(tenantId: string, userId: string): string {
  const privateKey = readFileSync(PRIVATE_KEY_PATH, "utf8");
  return jwt.sign({ tenantId }, privateKey, {
    algorithm: "RS256",
    subject: userId,
    expiresIn: "1h",
  });
}
```

- [ ] **Step 3: Write the login/navigation helper**

```ts
// apps/demo/e2e/helpers/login.ts
import type { Page } from "@playwright/test";

export async function loginAs(page: Page, token: string): Promise<void> {
  await page.goto("/dev-login");
  await page.getByLabel(/Paste a JWT/).fill(token);
  await page.getByRole("button", { name: "Use token" }).click();
  await page.waitForURL("/");
}

export async function goToCustomersList(page: Page): Promise<void> {
  await page.getByRole("link", { name: /Customer \(crm\.customers\)/ }).click();
  await page.waitForURL("/records/crm.customers");
  await page.waitForSelector("table");
}
```

- [ ] **Step 4: Write `playwright.config.ts`**

```ts
// apps/demo/playwright.config.ts
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  timeout: 30_000,
  use: {
    baseURL: "http://localhost:5173",
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: "pnpm --filter @metap/crm dev",
      url: "http://localhost:3000/health",
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
      cwd: "../..",
    },
    {
      command: "pnpm --filter @metap/demo dev",
      url: "http://localhost:5173",
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
      cwd: "../..",
    },
  ],
});
```

- [ ] **Step 5: Write `dev-login.spec.ts`**

```ts
// apps/demo/e2e/dev-login.spec.ts
import { test, expect } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs } from "./helpers/login";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test("dev-login with a valid token reaches the entities page", async ({ page }) => {
  const token = mintToken(TENANT_ID, ADMIN_USER_ID);
  await loginAs(page, token);
  await expect(page.getByRole("heading", { name: "Entities" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Customer \(crm\.customers\)/ })).toBeVisible();
});

test("visiting a protected route without a token redirects to dev-login", async ({ page }) => {
  await page.goto("/records/crm.customers");
  await expect(page.getByRole("heading", { name: "Dev Login" })).toBeVisible();
});
```

- [ ] **Step 6: Run it**

Ensure Postgres/RabbitMQ are up (`docker compose up -d postgres rabbitmq`, repo root) and the admin seed exists (see Global Constraints). Then:

```bash
cd apps/demo
pnpm exec playwright install chromium   # one-time, if not already cached
pnpm test:e2e
```

Expected: both tests pass. `webServer` will have started `apps/crm`/`apps/demo` itself if they weren't already running (visible in the Playwright output).

- [ ] **Step 7: Commit**

```bash
git add apps/demo/package.json package.json pnpm-lock.yaml \
  apps/demo/playwright.config.ts apps/demo/e2e/helpers/token.ts \
  apps/demo/e2e/helpers/login.ts apps/demo/e2e/dev-login.spec.ts
git commit -m "test(e2e): add Playwright infra and dev-login smoke test"
```

---

### Task 2: `helpers/api.ts` + `list.spec.ts`

**Files:**
- Create: `apps/demo/e2e/helpers/api.ts`
- Create: `apps/demo/e2e/list.spec.ts`

**Interfaces:**
- Consumes: `mintToken` (Task 1).
- Produces: `createAdminApiContext(adminToken: string): Promise<APIRequestContext>`, `createCustomer(api, data): Promise<{id, version}>`, `deleteCustomer(api, id, version): Promise<void>`, `getCustomerByCode(api, code): Promise<{id, version}>`, `grantRole(api, userId, role): Promise<void>`, `createPolicy(api, body): Promise<{id}>`, `deletePolicy(api, id): Promise<void>` — all from `helpers/api.ts`, used by every later spec file.

- [ ] **Step 1: Write the API helper**

```ts
// apps/demo/e2e/helpers/api.ts
import { request as playwrightRequest } from "@playwright/test";
import type { APIRequestContext } from "@playwright/test";

const API_BASE = "http://localhost:3000";

export async function createAdminApiContext(adminToken: string): Promise<APIRequestContext> {
  return playwrightRequest.newContext({
    baseURL: API_BASE,
    extraHTTPHeaders: { Authorization: `Bearer ${adminToken}` },
  });
}

export async function createCustomer(
  api: APIRequestContext,
  data: Record<string, unknown>,
): Promise<{ id: string; version: number }> {
  const res = await api.post("/api/crm.customers", { data: { data } });
  if (!res.ok()) {
    throw new Error(`createCustomer failed: ${res.status()} ${await res.text()}`);
  }
  const body = await res.json();
  return { id: body.data.id, version: body.data.version };
}

export async function deleteCustomer(
  api: APIRequestContext,
  id: string,
  version: number,
): Promise<void> {
  await api.delete(`/api/crm.customers/${id}`, { data: { version } });
}

export async function getCustomerByCode(
  api: APIRequestContext,
  code: string,
): Promise<{ id: string; version: number }> {
  const res = await api.get(`/api/crm.customers?code=${encodeURIComponent(code)}`);
  const body = await res.json();
  if (body.data.length === 0) {
    throw new Error(`No record found with code ${code}`);
  }
  return { id: body.data[0].id, version: body.data[0].version };
}

export async function grantRole(
  api: APIRequestContext,
  userId: string,
  role: string,
): Promise<void> {
  const res = await api.post(`/admin/users/${userId}/roles`, { data: { role } });
  if (!res.ok()) {
    throw new Error(`grantRole failed: ${res.status()} ${await res.text()}`);
  }
}

export async function createPolicy(
  api: APIRequestContext,
  body: {
    entity: string;
    action: "read" | "create" | "update" | "write" | "delete";
    field?: string;
    roles?: string[];
  },
): Promise<{ id: string }> {
  const res = await api.post("/admin/policies", { data: body });
  if (!res.ok()) {
    throw new Error(`createPolicy failed: ${res.status()} ${await res.text()}`);
  }
  const responseBody = await res.json();
  return { id: responseBody.data.id };
}

export async function deletePolicy(api: APIRequestContext, id: string): Promise<void> {
  await api.delete(`/admin/policies/${id}`);
}
```

- [ ] **Step 2: Write `list.spec.ts`**

```ts
// apps/demo/e2e/list.spec.ts
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs, goToCustomersList } from "./helpers/login";
import { createAdminApiContext, createCustomer, deleteCustomer } from "./helpers/api";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test.describe("GeneratedList", () => {
  let api: APIRequestContext;
  let runTag: string;
  let createdIds: { id: string; version: number }[] = [];

  test.beforeEach(async () => {
    runTag = randomUUID().slice(0, 8);
    api = await createAdminApiContext(mintToken(TENANT_ID, ADMIN_USER_ID));
    createdIds = [];
    for (let i = 0; i < 105; i++) {
      createdIds.push(
        await createCustomer(api, {
          code: `LIST-${runTag}-${String(i).padStart(4, "0")}`,
          name: `List Test ${runTag} ${i}`,
        }),
      );
    }
  });

  test.afterEach(async () => {
    for (const record of createdIds) {
      await deleteCustomer(api, record.id, record.version);
    }
    await api.dispose();
  });

  test("sort, filter, and infinite-scroll pagination work", async ({ page }) => {
    await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
    await goToCustomersList(page);

    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`List Test ${runTag}`);
    await page.waitForTimeout(600);
    const rowCountBeforeScroll = await page.locator("table tbody tr").count();
    expect(rowCountBeforeScroll).toBeGreaterThan(0);
    expect(rowCountBeforeScroll).toBeLessThan(105);

    const scrollContainer = page.locator('div[style*="overflow: auto"]').first();
    await scrollContainer.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await page.waitForTimeout(1000);
    const rowCountAfterScroll = await page.locator("table tbody tr").count();
    expect(rowCountAfterScroll).toBeGreaterThan(rowCountBeforeScroll);

    const nameHeader = page.locator("table thead tr").first().locator("th").nth(1);
    await nameHeader.click();
    const firstRowNameAsc = await page
      .locator("table tbody tr")
      .first()
      .locator("td")
      .nth(1)
      .textContent();
    await nameHeader.click();
    const firstRowNameDesc = await page
      .locator("table tbody tr")
      .first()
      .locator("td")
      .nth(1)
      .textContent();
    expect(firstRowNameAsc).not.toBe(firstRowNameDesc);
  });
});
```

- [ ] **Step 3: Run it**

```bash
cd apps/demo && pnpm test:e2e list.spec.ts
```

Expected: 1 test passes (may take a few seconds — 105 sequential record creates in `beforeEach`).

- [ ] **Step 4: Commit**

```bash
git add apps/demo/e2e/helpers/api.ts apps/demo/e2e/list.spec.ts
git commit -m "test(e2e): add API helper and list sort/filter/pagination coverage"
```

---

### Task 3: `record-crud.spec.ts` (create, reference picker, edit, delete)

**Files:**
- Create: `apps/demo/e2e/record-crud.spec.ts`

**Interfaces:**
- Consumes: everything from Tasks 1-2.

- [ ] **Step 1: Write the spec**

```ts
// apps/demo/e2e/record-crud.spec.ts
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs, goToCustomersList } from "./helpers/login";
import {
  createAdminApiContext,
  createCustomer,
  deleteCustomer,
  getCustomerByCode,
} from "./helpers/api";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test.describe("record CRUD", () => {
  let api: APIRequestContext;
  let runTag: string;
  const createdIds: { id: string; version: number }[] = [];

  test.beforeEach(async () => {
    runTag = randomUUID().slice(0, 8);
    api = await createAdminApiContext(mintToken(TENANT_ID, ADMIN_USER_ID));
  });

  test.afterEach(async () => {
    for (const record of createdIds.splice(0)) {
      const current = await api.get(`/api/crm.customers/${record.id}`);
      if (current.ok()) {
        const body = await current.json();
        if (!body.data.deleted) {
          await deleteCustomer(api, record.id, body.data.version);
        }
      }
    }
    await api.dispose();
  });

  test("create with the reference picker, view detail, and edit", async ({ page }) => {
    const referrer = await createCustomer(api, { code: `REF-${runTag}`, name: `Referrer ${runTag}` });
    createdIds.push(referrer);

    await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
    await goToCustomersList(page);

    await page.getByRole("link", { name: "New" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/new$/);
    await page.getByLabel("Code *").fill(`MAIN-${runTag}`);
    await page.getByLabel("Name *").fill(`Main ${runTag}`);
    await page.getByLabel("Email").fill(`main-${runTag}@example.com`);
    const referredByInput = page.getByRole("combobox", { name: "Referred By" });
    await referredByInput.click();
    await referredByInput.fill(`Referrer ${runTag}`);
    await expect(page.getByRole("option", { name: `Referrer ${runTag}` })).toBeVisible();
    await page.getByRole("option", { name: `Referrer ${runTag}` }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForURL("/records/crm.customers");

    const main = await getCustomerByCode(api, `MAIN-${runTag}`);
    createdIds.push(main);

    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`Main ${runTag}`);
    await page.waitForTimeout(600);
    await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);

    const referredByValue = page
      .locator("text=Referred By")
      .locator("xpath=following-sibling::*[1]");
    await expect(referredByValue).toHaveText(`Referrer ${runTag}`);

    await page.getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+\/edit$/);
    await page.getByLabel("Phone").fill("555-9999");
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
    await expect(page.getByText("555-9999")).toBeVisible();
  });

  test("delete removes the record from the list", async ({ page }) => {
    const record = await createCustomer(api, { code: `DEL-${runTag}`, name: `Delete Me ${runTag}` });
    createdIds.push(record);

    page.on("dialog", (dialog) => dialog.accept());
    await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
    await goToCustomersList(page);
    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`Delete Me ${runTag}`);
    await page.waitForTimeout(600);
    await expect(page.locator("table tbody tr")).toHaveCount(1);
    await page.locator("table tbody tr").first().getByRole("button", { name: "Delete" }).click();
    await expect(page.locator("table tbody tr", { hasText: `DEL-${runTag}` })).toHaveCount(0);
  });
});
```

- [ ] **Step 2: Run it**

```bash
cd apps/demo && pnpm test:e2e record-crud.spec.ts
```

Expected: 2 tests pass.

- [ ] **Step 3: Commit**

```bash
git add apps/demo/e2e/record-crud.spec.ts
git commit -m "test(e2e): add create/reference-picker/edit/delete coverage"
```

---

### Task 4: `workflow.spec.ts`

**Files:**
- Create: `apps/demo/e2e/workflow.spec.ts`

**Interfaces:**
- Consumes: everything from Tasks 1-2.

- [ ] **Step 1: Write the spec**

```ts
// apps/demo/e2e/workflow.spec.ts
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs, goToCustomersList } from "./helpers/login";
import { createAdminApiContext, deleteCustomer, createCustomer } from "./helpers/api";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test.describe("workflow transitions", () => {
  let api: APIRequestContext;
  let runTag: string;

  test.beforeEach(async () => {
    runTag = randomUUID().slice(0, 8);
    api = await createAdminApiContext(mintToken(TENANT_ID, ADMIN_USER_ID));
  });

  test.afterEach(async () => {
    await api.dispose();
  });

  test("Activate is guarded by email, then transitions the state", async ({ page }) => {
    const noEmail = await createCustomer(api, { code: `WF-NOEMAIL-${runTag}`, name: `No Email ${runTag}` });
    const withEmail = await createCustomer(api, {
      code: `WF-EMAIL-${runTag}`,
      name: `With Email ${runTag}`,
      email: `wf-${runTag}@example.com`,
    });

    await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
    await goToCustomersList(page);

    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`No Email ${runTag}`);
    await page.waitForTimeout(600);
    await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
    await expect(page.getByRole("button", { name: /^Activate/ })).toBeDisabled();

    await page.goBack();
    await page.waitForSelector("table");
    await nameFilter.fill(`With Email ${runTag}`);
    await page.waitForTimeout(600);
    await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);
    const activateButton = page.getByRole("button", { name: /^Activate/ });
    await expect(activateButton).toBeEnabled();
    await activateButton.click();
    await expect(page.getByText("ACTIVE")).toBeVisible();
    await expect(page.getByRole("button", { name: /^Block/ })).toBeVisible();

    await deleteCustomer(api, noEmail.id, noEmail.version);
    const current = await (await api.get(`/api/crm.customers/${withEmail.id}`)).json();
    await deleteCustomer(api, withEmail.id, current.data.version);
  });
});
```

- [ ] **Step 2: Run it**

```bash
cd apps/demo && pnpm test:e2e workflow.spec.ts
```

Expected: 1 test passes.

- [ ] **Step 3: Commit**

```bash
git add apps/demo/e2e/workflow.spec.ts
git commit -m "test(e2e): add guarded workflow transition coverage"
```

---

### Task 5: `permission-aware-ui.spec.ts` (field masking, part 1)

**Files:**
- Create: `apps/demo/e2e/permission-aware-ui.spec.ts`

**Interfaces:**
- Consumes: everything from Tasks 1-2, including `grantRole`/`createPolicy`/`deletePolicy`.

- [ ] **Step 1: Write the spec**

```ts
// apps/demo/e2e/permission-aware-ui.spec.ts
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs, goToCustomersList } from "./helpers/login";
import {
  createAdminApiContext,
  createCustomer,
  deleteCustomer,
  grantRole,
  createPolicy,
  deletePolicy,
} from "./helpers/api";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test.describe("permission-aware UI", () => {
  let api: APIRequestContext;
  let runTag: string;
  let viewerToken: string;
  let recordId: string;
  let emailPolicyId: string;
  let phonePolicyId: string;

  test.beforeEach(async () => {
    runTag = randomUUID().slice(0, 8);
    const viewerUserId = randomUUID();
    api = await createAdminApiContext(mintToken(TENANT_ID, ADMIN_USER_ID));
    viewerToken = mintToken(TENANT_ID, viewerUserId);
    await grantRole(api, viewerUserId, "viewer");

    emailPolicyId = (
      await createPolicy(api, { entity: "crm.customers", action: "read", field: "email", roles: ["admin"] })
    ).id;
    phonePolicyId = (
      await createPolicy(api, { entity: "crm.customers", action: "write", field: "phone", roles: ["admin"] })
    ).id;

    const record = await createCustomer(api, {
      code: `PERM-${runTag}`,
      name: `Perm Test ${runTag}`,
      email: `perm-${runTag}@example.com`,
    });
    recordId = record.id;
  });

  test.afterEach(async () => {
    await deletePolicy(api, emailPolicyId);
    await deletePolicy(api, phonePolicyId);
    const current = await api.get(`/api/crm.customers/${recordId}`);
    if (current.ok()) {
      const body = await current.json();
      if (!body.data.deleted) {
        await deleteCustomer(api, recordId, body.data.version);
      }
    }
    await api.dispose();
  });

  test("viewer role: email masked in list and detail, phone read-only, name editable", async ({
    page,
  }) => {
    await loginAs(page, viewerToken);
    await goToCustomersList(page);
    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`Perm Test ${runTag}`);
    await page.waitForTimeout(600);

    const emailCell = page.locator("table tbody tr").first().locator("td").nth(3);
    await expect(emailCell).toHaveText("—");

    await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);

    await page.getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+\/edit$/);
    await expect(page.getByLabel("Phone")).toBeDisabled();
    await expect(page.getByLabel("Name *")).toBeEnabled();
  });
});
```

- [ ] **Step 2: Run it**

```bash
cd apps/demo && pnpm test:e2e permission-aware-ui.spec.ts
```

Expected: 1 test passes.

- [ ] **Step 3: Commit**

```bash
git add apps/demo/e2e/permission-aware-ui.spec.ts
git commit -m "test(e2e): add field-level permission masking coverage"
```

---

### Task 6: Fix finding 3 — `ReferenceFieldValue` shows a loading state instead of the raw id

**Files:**
- Modify: `packages/platform-react/src/field/ReferenceFieldValue.tsx`
- Modify: `apps/demo/e2e/record-crud.spec.ts` (add a test)

**Interfaces:**
- No change to `ReferenceFieldValue`'s props (`{ field: EntityField; value: unknown }`).

- [ ] **Step 1: Write the failing test**

Add to `apps/demo/e2e/record-crud.spec.ts`, inside the existing `test.describe("record CRUD", ...)` block (after the "delete removes..." test):

```ts
  test("reference field never shows the raw id while its own fetch is loading", async ({ page }) => {
    const referrer = await createCustomer(api, { code: `REFB-${runTag}`, name: `Ref Target ${runTag}` });
    const referencing = await createCustomer(api, {
      code: `REFC-${runTag}`,
      name: `Ref Source ${runTag}`,
      referredBy: referrer.id,
    });
    createdIds.push(referrer, referencing);

    await page.route(`**/api/crm.customers/${referrer.id}`, async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 800));
      await route.continue();
    });

    await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
    await goToCustomersList(page);
    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`Ref Source ${runTag}`);
    await page.waitForTimeout(600);
    await page.locator("table tbody tr").first().getByRole("link", { name: "View" }).click();
    await page.waitForURL(/\/records\/crm\.customers\/[^/]+$/);

    const referredByValue = page
      .locator("text=Referred By")
      .locator("xpath=following-sibling::*[1]");
    await expect(referredByValue).not.toHaveText(referrer.id);
    await expect(referredByValue).toHaveText(`Ref Target ${runTag}`, { timeout: 2000 });
  });
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd apps/demo && pnpm test:e2e record-crud.spec.ts -g "never shows the raw id"
```

Expected: FAIL — the `not.toHaveText(referrer.id)` assertion fails because the field briefly (or, depending on timing, for the whole 800ms delay) shows the raw UUID.

- [ ] **Step 3: Fix `ReferenceFieldValue.tsx`**

Replace the whole file:

```tsx
// packages/platform-react/src/field/ReferenceFieldValue.tsx
import { useApiQuery } from "../api/useApiQuery";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

export function ReferenceFieldValue({ field, value }: { field: EntityField; value: unknown }) {
  const refEntity = field.refEntity;
  const id = typeof value === "string" ? value : undefined;

  const { data: record, isLoading } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, id],
    `/api/${refEntity}/${id}`,
    (response) => response.data,
    Boolean(refEntity && id),
  );

  if (!id) {
    return <>—</>;
  }

  if (isLoading) {
    return <>…</>;
  }

  const raw = record && field.refDisplayField ? record.data[field.refDisplayField] : undefined;
  return <>{typeof raw === "string" ? raw : id}</>;
}
```

The only change: `isLoading` is now destructured from `useApiQuery`'s result, and while it's `true` the component renders `…` instead of falling through to the `id` fallback — the raw id is now reserved for a genuine post-load failure (missing `refDisplayField` value), not the loading window.

- [ ] **Step 4: Run it to verify it passes**

```bash
cd apps/demo && pnpm test:e2e record-crud.spec.ts
```

Expected: all 3 tests in this file pass.

- [ ] **Step 5: Commit**

```bash
git add packages/platform-react/src/field/ReferenceFieldValue.tsx apps/demo/e2e/record-crud.spec.ts
git commit -m "fix(platform-react): ReferenceFieldValue shows a loading state, not the raw id"
```

---

### Task 7: Fix findings 2 + 4 — styled errors with a back-to-list link, always-visible back-to-list navigation

**Files:**
- Modify: `packages/platform-react/src/api/ApiErrorMessage.tsx`
- Modify: `packages/platform-react/src/detail/RecordDetail.tsx`
- Modify: `packages/platform-react/src/form/GeneratedForm.tsx`
- Modify: `apps/demo/e2e/record-crud.spec.ts` (add back-to-list/cancel-link assertions)
- Create: `apps/demo/e2e/error-states.spec.ts`

**Interfaces:**
- Produces: `ApiErrorMessage`'s new optional prop `backTo?: { to: string; label: string }`.

- [ ] **Step 1: Write the failing tests**

Add to `apps/demo/e2e/record-crud.spec.ts`'s first test (`"create with the reference picker, view detail, and edit"`), right after the existing `referredByValue` assertion:

```ts
    await expect(page.getByRole("link", { name: "Back to list" })).toBeVisible();
```

And right after the `page.waitForURL(/\/records\/crm\.customers\/[^/]+\/edit$/);` line in the same test:

```ts
    await expect(page.getByRole("link", { name: "Cancel" })).toBeVisible();
```

Create `apps/demo/e2e/error-states.spec.ts`:

```ts
// apps/demo/e2e/error-states.spec.ts
import { test, expect } from "@playwright/test";
import { mintToken } from "./helpers/token";
import { loginAs, goToCustomersList } from "./helpers/login";

const TENANT_ID = "00000000-0000-0000-0000-000000000001";
const ADMIN_USER_ID = "00000000-0000-0000-0000-000000000002";

test("a not-found record shows a styled error with a way back to the list", async ({ page }) => {
  await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
  await goToCustomersList(page);
  await page.evaluate(() => {
    window.history.pushState({}, "", "/records/crm.customers/00000000-0000-0000-0000-000000000000");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await expect(page.getByText(/Record not found/)).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to list" })).toBeVisible();
  await page.getByRole("link", { name: "Back to list" }).click();
  await page.waitForURL("/records/crm.customers");
});
```

- [ ] **Step 2: Run them to verify they fail**

```bash
cd apps/demo && pnpm test:e2e record-crud.spec.ts error-states.spec.ts
```

Expected: the record-crud "Back to list"/"Cancel" assertions and the new error-states test all FAIL (none of those links exist yet).

- [ ] **Step 3: Add the `backTo` prop to `ApiErrorMessage`**

Replace the whole file:

```tsx
// packages/platform-react/src/api/ApiErrorMessage.tsx
import { Alert, Anchor } from "@mantine/core";
import { useNavigationAdapter } from "../navigation/NavigationContext";
import { ApiError } from "./client";

export function ApiErrorMessage({
  error,
  backTo,
}: {
  error: unknown;
  backTo?: { to: string; label: string };
}) {
  const adapter = useNavigationAdapter();

  if (error instanceof ApiError && error.status === 401) {
    return (
      <Alert color="red" title="Session expired">
        <Anchor component={adapter.Link} to={adapter.toLogin()}>
          Sign in again
        </Anchor>
        .
      </Alert>
    );
  }

  return (
    <Alert color="red" title="Something went wrong">
      <div>{error instanceof Error ? error.message : String(error)}</div>
      {backTo ? (
        <Anchor component={adapter.Link} to={backTo.to}>
          {backTo.label}
        </Anchor>
      ) : null}
    </Alert>
  );
}
```

- [ ] **Step 4: Add a permanent back-link and `backTo` to `RecordDetail`**

In `packages/platform-react/src/detail/RecordDetail.tsx`, change the two `ApiErrorMessage` calls:

```tsx
  if (entityError) {
    return (
      <ApiErrorMessage
        error={entityError}
        backTo={{ to: navAdapter.toRecordList(entityName), label: "Back to list" }}
      />
    );
  }
  if (recordError) {
    return (
      <ApiErrorMessage
        error={recordError}
        backTo={{ to: navAdapter.toRecordList(entityName), label: "Back to list" }}
      />
    );
  }
```

And change the final `<Group mt="md">` block to add a permanent back-link before Edit:

```tsx
      <Group mt="md">
        <Anchor component={navAdapter.Link} to={navAdapter.toRecordList(entityName)}>
          Back to list
        </Anchor>
        <Anchor component={navAdapter.Link} to={navAdapter.toEditRecord(entityName, id)}>
          Edit
        </Anchor>
        <Button
          color="red"
          variant="subtle"
          size="compact-sm"
          loading={deleting}
          onClick={() => void handleDelete()}
        >
          Delete
        </Button>
      </Group>
```

- [ ] **Step 5: Add a permanent "Cancel" link and `backTo` to `GeneratedForm`**

In `packages/platform-react/src/form/GeneratedForm.tsx`, add the navigation adapter import and hook:

```tsx
import { Alert, Anchor, Button, Container, Group, Stack, Title } from "@mantine/core";
```

(replacing the existing `import { Alert, Button, Container, Stack, Title } from "@mantine/core";`)

```tsx
import { useNavigationAdapter } from "../navigation/NavigationContext";
```

(new import, alongside the other existing imports)

Inside the component, right after `const { data: entity, ... } = useEntity(entityName);`:

```tsx
  const navAdapter = useNavigationAdapter();
```

Change the `existingError` branch:

```tsx
  if (recordId && existingError) {
    return (
      <ApiErrorMessage
        error={existingError}
        backTo={{ to: navAdapter.toRecordList(entityName), label: "Back to list" }}
      />
    );
  }
```

And change the final `<Stack>`'s closing button block from:

```tsx
        <Button onClick={() => void handleSubmit()} loading={submitting}>
          Save
        </Button>
      </Stack>
```

to:

```tsx
        <Group>
          <Button onClick={() => void handleSubmit()} loading={submitting}>
            Save
          </Button>
          <Anchor component={navAdapter.Link} to={navAdapter.toRecordList(entityName)}>
            Cancel
          </Anchor>
        </Group>
      </Stack>
```

- [ ] **Step 6: Run the tests again to verify they pass**

```bash
cd apps/demo && pnpm test:e2e record-crud.spec.ts error-states.spec.ts
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add packages/platform-react/src/api/ApiErrorMessage.tsx \
  packages/platform-react/src/detail/RecordDetail.tsx \
  packages/platform-react/src/form/GeneratedForm.tsx \
  apps/demo/e2e/record-crud.spec.ts apps/demo/e2e/error-states.spec.ts
git commit -m "fix(platform-react): styled errors with a back-to-list link; permanent back-link on detail/form"
```

---

### Task 8: Fix finding 1 — friendly required-field validation messages

**Files:**
- Modify: `packages/platform-react/src/form/GeneratedForm.tsx`
- Modify: `apps/demo/e2e/error-states.spec.ts` (add a test)

- [ ] **Step 1: Write the failing test**

Add to `apps/demo/e2e/error-states.spec.ts`:

```ts
test("required-field validation shows a friendly message, not a raw Zod message", async ({ page }) => {
  await loginAs(page, mintToken(TENANT_ID, ADMIN_USER_ID));
  await goToCustomersList(page);
  await page.getByRole("link", { name: "New" }).click();
  await page.waitForURL(/\/records\/crm\.customers\/new$/);
  await page.getByRole("button", { name: "Save" }).click();

  await expect(page.getByText("Code is required.")).toBeVisible();
  await expect(page.getByText("Name is required.")).toBeVisible();
  await expect(page.getByText(/Invalid input/)).not.toBeVisible();
});
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd apps/demo && pnpm test:e2e error-states.spec.ts -g "friendly message"
```

Expected: FAIL — the current message is "Invalid input: expected string, received undefined".

- [ ] **Step 3: Add client-side required-field validation to `GeneratedForm`**

In `packages/platform-react/src/form/GeneratedForm.tsx`, change the start of `handleSubmit`:

```tsx
  async function handleSubmit() {
    setFormError(null);
    setFieldErrors({});

    const missingRequired = entity!.fields.filter(
      (field) => field.required && isEmptyValue(formData[field.name]),
    );
    if (missingRequired.length > 0) {
      setFieldErrors(
        Object.fromEntries(missingRequired.map((f) => [f.name, [`${f.label} is required.`]])),
      );
      return;
    }

    const payload: Record<string, unknown> = {};
```

(the rest of `handleSubmit` — the `for (const [key, value] of Object.entries(formData))` loop through the `try`/`catch` block — is unchanged)

Add a small helper function near the bottom of the file, after the component:

```tsx
function isEmptyValue(value: unknown): boolean {
  return value === undefined || value === null || (typeof value === "string" && value.trim() === "");
}
```

- [ ] **Step 4: Run it to verify it passes**

```bash
cd apps/demo && pnpm test:e2e error-states.spec.ts
```

Expected: both tests in this file pass.

- [ ] **Step 5: Commit**

```bash
git add packages/platform-react/src/form/GeneratedForm.tsx apps/demo/e2e/error-states.spec.ts
git commit -m "fix(platform-react): friendly required-field messages instead of raw Zod errors"
```

---

### Task 9: Fix finding 5 — allow a `delete`-action policy to be created

**Files:**
- Modify: `packages/core/src/server/routes/admin.ts`
- Modify: `apps/demo/e2e/permission-aware-ui.spec.ts` (add a test)

- [ ] **Step 1: Write the failing test**

Add to `apps/demo/e2e/permission-aware-ui.spec.ts`, inside the existing `test.describe`. Change `beforeEach` to also create a delete-restricting policy, and `afterEach` to also clean it up:

```ts
  let deletePolicyId: string;
```

(add this `let` alongside the other `let` declarations)

In `beforeEach`, after the `phonePolicyId` assignment:

```ts
    deletePolicyId = (
      await createPolicy(api, { entity: "crm.customers", action: "delete", roles: ["admin"] })
    ).id;
```

In `afterEach`, after `await deletePolicy(api, phonePolicyId);`:

```ts
    await deletePolicy(api, deletePolicyId);
```

Add a new test after the existing one:

```ts
  test("viewer role: delete is rejected when a delete policy restricts it to admin", async ({ page }) => {
    page.on("dialog", (dialog) => dialog.accept());
    await loginAs(page, viewerToken);
    await goToCustomersList(page);
    const nameFilter = page.locator("table thead tr").nth(1).locator("th").nth(1).locator("input");
    await nameFilter.fill(`Perm Test ${runTag}`);
    await page.waitForTimeout(600);

    await page.locator("table tbody tr").first().getByRole("button", { name: "Delete" }).click();
    await page.waitForTimeout(500);

    const current = await api.get(`/api/crm.customers/${recordId}`);
    const body = await current.json();
    expect(body.data.deleted).toBe(false);
  });
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd apps/demo && pnpm test:e2e permission-aware-ui.spec.ts -g "delete is rejected"
```

Expected: FAIL two ways — first, `createPolicy` in `beforeEach` itself throws (`action: "delete"` is rejected by `CreatePolicyBodySchema` with a 400), which fails every test in the file, not just the new one. Confirm this is the failure mode (a thrown setup error), not a passing-for-the-wrong-reason test.

- [ ] **Step 3: Widen `CreatePolicyBodySchema`'s action enum**

In `packages/core/src/server/routes/admin.ts`, change:

```ts
const CreatePolicyBodySchema = z
  .object({
    entity: z.string().min(1),
    action: z.enum(["read", "create", "update", "write"]),
    roles: z.array(z.string()).optional(),
    condition: PolicyConditionSchema.optional(),
    field: z.string().optional(),
    subject: z.enum(["context", "record"]).optional(),
  })
  .refine(
    (body) =>
      body.field
        ? body.action === "read" || body.action === "write"
        : body.action === "read" || body.action === "create" || body.action === "update",
    {
      message:
        'A field-scoped policy requires action "read" or "write"; an entity-scoped policy (no field) requires action "read", "create", or "update".',
      path: ["action"],
    },
  );
```

to:

```ts
const CreatePolicyBodySchema = z
  .object({
    entity: z.string().min(1),
    action: z.enum(["read", "create", "update", "write", "delete"]),
    roles: z.array(z.string()).optional(),
    condition: PolicyConditionSchema.optional(),
    field: z.string().optional(),
    subject: z.enum(["context", "record"]).optional(),
  })
  .refine(
    (body) =>
      body.field
        ? body.action === "read" || body.action === "write"
        : body.action === "read" ||
          body.action === "create" ||
          body.action === "update" ||
          body.action === "delete",
    {
      message:
        'A field-scoped policy requires action "read" or "write"; an entity-scoped policy (no field) requires action "read", "create", "update", or "delete".',
      path: ["action"],
    },
  );
```

Also change `ExplainBodySchema`'s `action` field:

```ts
  action: z.enum(["read", "create", "update", "write"]),
```

to:

```ts
  action: z.enum(["read", "create", "update", "write", "delete"]),
```

(No other code changes needed: `PermissionService.canDeleteEntity`/`checkAction` already accepts `EntityAction` including `"delete"` and queries the `policies` table by that action string; `PolicyStore.createPolicy`'s `action` parameter is already typed as plain `string`; the `policies.action` DB column is an unconstrained `varchar(20)`, so no migration is needed — confirmed by reading `packages/core/src/core/permission/policy-service.ts` and `packages/core/src/infra/db/schema.ts` during this plan's research.)

- [ ] **Step 4: Run it to verify it passes**

```bash
cd apps/demo && pnpm test:e2e permission-aware-ui.spec.ts
```

Expected: both tests in this file pass.

- [ ] **Step 5: Run the full backend test suite to check for regressions**

```bash
pnpm --filter @metap/core test
```

Expected: all existing tests still pass — in particular `packages/core/src/server/routes/admin.test.ts`, which exercises `CreatePolicyBodySchema`'s validation directly.

- [ ] **Step 6: Commit**

```bash
git add packages/core/src/server/routes/admin.ts apps/demo/e2e/permission-aware-ui.spec.ts
git commit -m "fix(core): allow delete-action policies so delete can be permission-restricted"
```

---

### Task 10: Full suite run + update `docs/bugs/2026-08-02.md`

**Files:**
- Modify: `docs/bugs/2026-08-02.md`

- [ ] **Step 1: Run the entire suite**

```bash
cd apps/demo && pnpm test:e2e
```

Expected: every test in every spec file passes.

- [ ] **Step 2: Run the full project verification**

```bash
cd /home/minhtuan/dev/local/metap
pnpm typecheck
pnpm --filter @metap/core test
pnpm lint
```

Expected: all pass. Fix anything that doesn't before continuing.

- [ ] **Step 3: Mark all 5 findings as closed in `docs/bugs/2026-08-02.md`**

For each of the 5 numbered findings, add a line right after its `**Likely location:**` line:

```markdown
- **Status:** Closed — `apps/demo/e2e/<relevant spec file>.spec.ts` (Task N of `docs/superpowers/plans/2026-08-03-fe-e2e-test-suite-plan.md`).
```

using the actual task number and spec file each finding was closed in (3 → Task 6/`record-crud.spec.ts`; 2 and 4 → Task 7/`error-states.spec.ts` + `record-crud.spec.ts`; 1 → Task 8/`error-states.spec.ts`; 5 → Task 9/`permission-aware-ui.spec.ts`).

- [ ] **Step 4: Commit**

```bash
git add docs/bugs/2026-08-02.md
git commit -m "docs: mark all 5 FE verification findings closed by the e2e suite"
```
