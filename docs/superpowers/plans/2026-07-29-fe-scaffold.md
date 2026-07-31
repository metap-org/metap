# Frontend Slice #1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up a working React frontend (`web/`) that can authenticate against metap's verify-only backend via a temporary dev-login screen, call the real API through a typed client, and prove the whole chain works by listing entities and rendering one real record list.

**Architecture:** `web/` is a single Vite package (not a nested workspace) with two clearly separated source folders: `web/src/platform/` (api-client, metadata-client, auth context — the reusable pieces a future downstream project would import) and `web/src/demo/` (dev-login screen, entities landing page, customers list — a development harness for the platform code, not a real business app). The backend gains one small, standalone dev-tool script (`scripts/mint-dev-token.mjs`) and no routes or runtime behavior changes.

**Tech Stack:** Vite, React, TypeScript, Mantine (UI), TanStack Query (server state), React Router (routing).

## Global Constraints

- **Do not commit anything at any point in this plan.** Per CLAUDE.md, leave every change in the working tree for the user to review. Never run `git commit`.
- Token storage is **in-memory only** (a React context) — never `localStorage`, never `sessionStorage`. This is a deliberate, direct departure from the legacy system's pattern audited earlier in this project.
- `web/src/platform/*` holds only reusable pieces (api-client, metadata-client, auth context). `web/src/demo/*` holds the dev-login screen and the two demo pages. Don't mix them into one folder.
- No backend route changes. The only backend change is the new standalone script in `scripts/` plus one `package.json` script entry — it does not run inside the server process and does not change what the backend accepts or verifies.
- No automated tests for this plan — it's scaffolding (build tooling, routing, a dev-only auth shim, a thin fetch wrapper, two demo pages), not business logic with edge cases worth locking in. Verification is manual: run the dev servers and click through the real flow. Where a real browser/screenshot tool is available in your environment, use it per the project's own convention (verify UI changes in a real browser, not just by reasoning about the code). Where none is available, verify via `curl` against the running dev server and a clean `pnpm build` (which runs `tsc -b` first) — both catch most integration errors even without a visual check.
- Two pre-existing, unrelated backend typecheck errors exist in this repo (`src/infra/messaging/rabbitmq.ts`) — irrelevant to this plan (nothing here touches backend TypeScript compilation), don't worry about them.

---

### Task 1: Backend dev-token minting script

**Files:**
- Create: `scripts/mint-dev-token.mjs`
- Modify: `package.json` (add a script entry)

**Interfaces:**
- Produces: a CLI usable as `pnpm mint-token [tenantId] [userId] [roles]`, printing a signed RS256 JWT to stdout.

- [ ] **Step 1: Write `scripts/mint-dev-token.mjs`**

```js
import jwt from "jsonwebtoken";
import { readFileSync } from "node:fs";

const tenantId = process.argv[2] ?? "00000000-0000-0000-0000-000000000001";
const userId = process.argv[3] ?? "00000000-0000-0000-0000-000000000002";
const roles = (process.argv[4] ?? "admin").split(",");

const privateKey = readFileSync("keys/dev-jwt-private.pem", "utf8");

const token = jwt.sign({ tenantId, roles }, privateKey, {
  algorithm: "RS256",
  subject: userId,
  expiresIn: "1h",
});

console.log(token);
```

This reads the same `keys/dev-jwt-private.pem` that `pnpm auth:dev-keys` (from an earlier plan) already generates. If that file doesn't exist in your environment, run `pnpm auth:dev-keys` first.

- [ ] **Step 2: Add the script entry to `package.json`**

Add to `"scripts"`:
```json
    "mint-token": "node scripts/mint-dev-token.mjs",
```

- [ ] **Step 3: Verify**

Run: `pnpm mint-token`
Expected: prints a JWT string (three base64url segments separated by dots), no errors.

Run: `pnpm mint-token 00000000-0000-0000-0000-000000000005 00000000-0000-0000-0000-000000000006 viewer,editor`
Expected: prints a different JWT (different claims — you can spot-check by decoding the middle segment with `node -e "console.log(JSON.parse(Buffer.from(process.argv[1], 'base64url')))" <middle-segment>` if you want to confirm the custom args landed in the payload, though this isn't required).

- [ ] **Step 4: Leave uncommitted**

Per Global Constraints, do not commit. Confirm via `git status` that `scripts/mint-dev-token.mjs` and `package.json` show as new/modified, and stop there.

---

### Task 2: Frontend scaffold (Vite + React + TypeScript + Mantine + TanStack Query + React Router)

**Files:**
- Create: `web/` (entire new Vite project — package.json, vite.config.ts, tsconfig files, index.html, src/main.tsx, src/App.tsx, and whatever else the Vite scaffold generates)

**Interfaces:**
- Produces: a Vite dev server on port 5173 that boots to a placeholder page, with `/api`, `/metadata`, `/health` proxied to `http://localhost:3000`, Mantine and TanStack Query wired at the provider level, React Router wired with an empty route table (routes come in later tasks).

- [ ] **Step 1: Scaffold the Vite project**

From the repo root:
```bash
mkdir web
cd web
pnpm create vite@latest . -- --template react-ts
```

If this prompts interactively despite the `--template` flag (depends on the installed `create-vite` version), answer: Framework = React, Variant = TypeScript.

- [ ] **Step 2: Install dependencies**

Still inside `web/`:
```bash
pnpm install
pnpm add @mantine/core @mantine/hooks @mantine/notifications @tanstack/react-query react-router-dom
pnpm add -D postcss postcss-preset-mantine postcss-simple-vars
```

- [ ] **Step 3: Add Mantine's PostCSS config**

Create `web/postcss.config.cjs`:
```js
module.exports = {
  plugins: {
    "postcss-preset-mantine": {},
    "postcss-simple-vars": {
      variables: {
        "mantine-breakpoint-xs": "36em",
        "mantine-breakpoint-sm": "48em",
        "mantine-breakpoint-md": "62em",
        "mantine-breakpoint-lg": "75em",
        "mantine-breakpoint-xl": "88em",
      },
    },
  },
};
```

- [ ] **Step 4: Configure the dev server proxy**

Replace `web/vite.config.ts`'s content with:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:3000",
      "/metadata": "http://localhost:3000",
      "/health": "http://localhost:3000",
    },
  },
});
```

(If the scaffold generated a slightly different default content — e.g. extra comments — this replacement is fine; the important part is the `server.proxy` block and keeping the `react()` plugin.)

- [ ] **Step 5: Wire providers in the entry point**

Replace `web/src/main.tsx`'s content with:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import App from "./App";

const queryClient = new QueryClient();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MantineProvider>
      <Notifications />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
```

- [ ] **Step 6: Replace the placeholder `App.tsx`**

Delete `web/src/App.css` (the scaffold's default styling isn't needed since Mantine provides styling). Replace `web/src/App.tsx`'s content with:

```tsx
export default function App() {
  return <div>metap web scaffold</div>;
}
```

- [ ] **Step 7: Verify it boots**

Run: `pnpm build` (inside `web/`)
Expected: succeeds with no TypeScript errors (this runs `tsc -b` before bundling, per the Vite react-ts template's default build script).

Run: `pnpm dev` (inside `web/`, in the background or a separate terminal)
Expected: starts on `http://localhost:5173`. If you have browser/screenshot tooling available, open it and confirm you see the text "metap web scaffold" with no console errors. If not, `curl -s http://localhost:5173` and confirm you get back HTML (the Vite dev server's index page), and check the terminal output for the dev server for any startup errors.

Stop the dev server when done checking.

- [ ] **Step 8: Leave uncommitted**

Do not commit. Confirm via `git status` that the entire `web/` directory shows as new/untracked (its own `.gitignore`, generated by the scaffold, already excludes `web/node_modules` and `web/dist`).

---

### Task 3: Auth context, api-client, dev-login screen

**Files:**
- Create: `web/src/platform/auth/AuthContext.tsx`
- Create: `web/src/platform/api/client.ts`
- Create: `web/src/demo/DevLoginPage.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: nothing from earlier tasks except the scaffold itself (Task 2).
- Produces: `AuthProvider` (React component) and `useAuth(): { token: string | null; setToken: (token: string | null) => void }`, both from `web/src/platform/auth/AuthContext.tsx`. `ApiError` (class, `status`/`code`/`message`) and `apiFetch<T>(path: string, token: string | null, init?: RequestInit): Promise<T>`, both from `web/src/platform/api/client.ts`. These are what Task 4 imports.

- [ ] **Step 1: Write the auth context, `web/src/platform/auth/AuthContext.tsx`**

```tsx
import { createContext, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";

type AuthContextValue = {
  token: string | null;
  setToken: (token: string | null) => void;
};

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(null);
  const value = useMemo(() => ({ token, setToken }), [token]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);

  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }

  return context;
}
```

- [ ] **Step 2: Write the api client, `web/src/platform/api/client.ts`**

```ts
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

type ErrorBody = {
  error: {
    code: string;
    message: string;
    requestId: string;
    traceId: string;
  };
};

export async function apiFetch<T>(
  path: string,
  token: string | null,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });

  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as ErrorBody | null;

    if (body?.error) {
      throw new ApiError(response.status, body.error.code, body.error.message);
    }

    throw new ApiError(response.status, "unknown_error", response.statusText);
  }

  return (await response.json()) as T;
}
```

- [ ] **Step 3: Write the dev-login page, `web/src/demo/DevLoginPage.tsx`**

```tsx
import { useState } from "react";
import { Button, Container, Textarea, Title } from "@mantine/core";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../platform/auth/AuthContext";

export function DevLoginPage() {
  const [value, setValue] = useState("");
  const { setToken } = useAuth();
  const navigate = useNavigate();

  function handleSubmit() {
    setToken(value.trim());
    navigate("/");
  }

  return (
    <Container size="sm" py="xl">
      <Title order={2} mb="md">
        Dev Login
      </Title>
      <Textarea
        label="Paste a JWT minted with `pnpm mint-token` (run in the backend repo)"
        minRows={4}
        value={value}
        onChange={(event) => setValue(event.currentTarget.value)}
      />
      <Button mt="md" onClick={handleSubmit} disabled={value.trim().length === 0}>
        Use token
      </Button>
    </Container>
  );
}
```

(If the installed Mantine version's `Textarea`/`Button` prop names or event-handler signature differ slightly from what's shown, adjust to match — the intent is a controlled multi-line text input and a submit button, nothing more exotic.)

- [ ] **Step 4: Wire routing and a `RequireAuth` guard in `web/src/App.tsx`**

Replace `web/src/App.tsx`'s content with:

```tsx
import type { ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./platform/auth/AuthContext";
import { DevLoginPage } from "./demo/DevLoginPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/dev-login" replace />;
  }

  return <>{children}</>;
}

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/dev-login" element={<DevLoginPage />} />
        <Route
          path="/"
          element={
            <RequireAuth>
              <div>Logged in placeholder — Task 4 replaces this</div>
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
```

- [ ] **Step 5: Verify**

Run: `pnpm build` (inside `web/`)
Expected: no TypeScript errors.

Run: `pnpm dev` (inside `web/`). Navigate to (or, without a browser, `curl -s http://localhost:5173/` and confirm HTML comes back — the actual client-side redirect only happens in the browser's JS, so a curl check here mainly confirms the server itself is up; a real browser check is more meaningful for this task specifically):
1. Visiting `/` with no token redirects to `/dev-login` (client-side redirect, needs a real browser or headless browser tool to observe — if you have one, use it).
2. Pasting any non-empty text into the textarea and clicking "Use token" navigates to `/` and shows "Logged in placeholder — Task 4 replaces this".

Stop the dev server when done.

- [ ] **Step 6: Leave uncommitted**

Do not commit.

---

### Task 4: Metadata client, demo pages, full end-to-end verification

**Files:**
- Create: `web/src/platform/metadata/useEntities.ts`
- Create: `web/src/demo/EntitiesPage.tsx`
- Create: `web/src/demo/CustomersPage.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `useAuth` (Task 3), `apiFetch`/`ApiError` (Task 3).
- Produces: `useEntities()` TanStack Query hook.

- [ ] **Step 1: Write the metadata client, `web/src/platform/metadata/useEntities.ts`**

```ts
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch } from "../api/client";

export type EntityField = {
  name: string;
  label: string;
  kind: string;
  required?: boolean;
  searchable?: boolean;
  sortable?: boolean;
};

export type EntityListView = {
  name: string;
  label: string;
  fields: readonly string[];
  filters: readonly string[];
  defaultSort?: string;
  maxLimit: number;
};

export type EntitySummary = {
  name: string;
  label: string;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: unknown;
};

export function useEntities() {
  const { token } = useAuth();

  return useQuery({
    queryKey: ["entities"],
    queryFn: () => apiFetch<{ data: EntitySummary[] }>("/metadata/entities", token),
    select: (response) => response.data,
    enabled: token !== null,
  });
}
```

- [ ] **Step 2: Write the entities landing page, `web/src/demo/EntitiesPage.tsx`**

```tsx
import { Anchor, Container, List, Title } from "@mantine/core";
import { Link } from "react-router-dom";
import { useEntities } from "../platform/metadata/useEntities";

export function EntitiesPage() {
  const { data, isLoading, error } = useEntities();

  if (isLoading) return <div>Loading...</div>;
  if (error) return <div>Error: {(error as Error).message}</div>;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        Entities
      </Title>
      <List>
        {data?.map((entity) => (
          <List.Item key={entity.name}>
            <Anchor component={Link} to="/customers">
              {entity.label} ({entity.name})
            </Anchor>
          </List.Item>
        ))}
      </List>
    </Container>
  );
}
```

This links every listed entity to `/customers` regardless of which one it is — acceptable simplification for this slice, since `crm.customers` is the only registered entity. A real per-entity route is `GeneratedList`'s job, not this slice's.

- [ ] **Step 3: Write the customers list page, `web/src/demo/CustomersPage.tsx`**

```tsx
import { useQuery } from "@tanstack/react-query";
import { Container, Table, Title } from "@mantine/core";
import { useAuth } from "../platform/auth/AuthContext";
import { apiFetch } from "../platform/api/client";

type CustomerRecord = {
  id: string;
  code: string | null;
  status: string | null;
  data: { name?: string };
};

export function CustomersPage() {
  const { token } = useAuth();

  const { data, isLoading, error } = useQuery({
    queryKey: ["records", "crm.customers"],
    queryFn: () => apiFetch<{ data: CustomerRecord[] }>("/api/crm.customers?limit=30", token),
    select: (response) => response.data,
    enabled: token !== null,
  });

  if (isLoading) return <div>Loading...</div>;
  if (error) return <div>Error: {(error as Error).message}</div>;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        Customers
      </Title>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Code</Table.Th>
            <Table.Th>Name</Table.Th>
            <Table.Th>Status</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {data?.map((record) => (
            <Table.Tr key={record.id}>
              <Table.Td>{record.code}</Table.Td>
              <Table.Td>{record.data.name}</Table.Td>
              <Table.Td>{record.status}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Container>
  );
}
```

- [ ] **Step 4: Wire the two new pages into routing, `web/src/App.tsx`**

Replace the file's content with:

```tsx
import type { ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./platform/auth/AuthContext";
import { DevLoginPage } from "./demo/DevLoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";
import { CustomersPage } from "./demo/CustomersPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/dev-login" replace />;
  }

  return <>{children}</>;
}

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/dev-login" element={<DevLoginPage />} />
        <Route
          path="/"
          element={
            <RequireAuth>
              <EntitiesPage />
            </RequireAuth>
          }
        />
        <Route
          path="/customers"
          element={
            <RequireAuth>
              <CustomersPage />
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
```

- [ ] **Step 5: Typecheck the frontend**

Run: `pnpm build` (inside `web/`)
Expected: no TypeScript errors.

- [ ] **Step 6: Full end-to-end manual verification**

Bring up the backend (in the repo root, a separate terminal from `web/`):
```bash
docker compose up -d postgres rabbitmq
pnpm db:migrate
pnpm dev
```

Mint a token (repo root):
```bash
pnpm mint-token
```
Copy the printed token.

Start the frontend (inside `web/`, another terminal):
```bash
pnpm dev
```

Using a real browser if available (per this project's convention of verifying UI changes visually, not just by reading code):
1. Open `http://localhost:5173/` — should redirect to `/dev-login`.
2. Paste the minted token into the textarea, click "Use token" — should navigate to `/` and show the Entities page listing `Customer (crm.customers)`.
3. Click the `Customer (crm.customers)` link — should navigate to `/customers` and show a table. If no customer records exist yet, the table will just have a header row with no data rows — that's fine, it proves the request succeeded with an empty/short list rather than proving nothing.
4. To see an actual row: in a third terminal, create one via the existing REST API (reusing the token from above):
   ```bash
   curl -s -X POST -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
     -d '{"data":{"code":"WEB1","name":"Web Test Co"}}' \
     http://localhost:3000/api/crm.customers
   ```
   Reload `/customers` in the browser — the new row should now appear.
5. Clean up the test record (repo root, or via `docker compose exec`):
   ```bash
   docker compose exec -T postgres psql -U metap -d metap -c "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM records WHERE code = 'WEB1'); DELETE FROM records WHERE code = 'WEB1';"
   ```

If no real browser/screenshot tool is available in your environment, do the equivalent checks via `curl` against the backend directly (confirming `/metadata/entities` and `/api/crm.customers` both return the expected JSON with the minted token) and note in your report that the frontend's own rendering could not be visually confirmed, rather than claiming it was.

Stop both dev servers when done.

- [ ] **Step 7: Leave uncommitted**

Do not commit. Confirm via `git status` that everything from this whole plan (Tasks 1-4) shows as new/modified, and stop there — the user reviews and commits.
