# Navigation Decoupling for `packages/platform-react` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `react-router-dom` and hardcoded `apps/demo` URL paths from `packages/platform-react`'s components, replacing both with a small injected `NavigationAdapter`, so a differently-built consumer (different router, different URL scheme, or a micro-frontend host) could use `ApiErrorMessage`/`GeneratedList`/`RecordDetail` without modification.

**Architecture:** A `NavigationAdapter` interface + React Context lives in `packages/platform-react`, covering exactly the 5 navigation actions these components need (list/new/detail/edit/login). `apps/demo` implements it once, backed by `react-router-dom`, encoding its own URL scheme — this file is the reference recipe a future differently-built consumer copies and adapts, not a generalized abstraction platform-react ships for them.

**Tech Stack:** React 19, `react-router-dom` (moves from a component-level import to an `apps/demo`-only adapter implementation), no new dependency.

## Global Constraints

- No new dependency.
- `useNavigationAdapter()` throws a clear error if no `NavigationContext.Provider` is above it in the tree — never a silent default (matches this session's established `PermissionService.scopedTenant` fix).
- Do not touch `GeneratedForm.tsx` or `WorkflowActionBar.tsx` — confirmed neither imports `react-router-dom` (`GeneratedForm` already takes an `onSaved` callback prop; `WorkflowActionBar` never navigates).
- `packages/platform-react` has no test framework — verification is `pnpm typecheck`/`pnpm build` across `packages/platform-react` and `apps/demo`, plus `pnpm lint`, plus a best-effort manual browser check (this sandbox has had no working headless Chromium all session — report that honestly if still true, don't claim success without it).

---

### Task 1: `NavigationAdapter` + rewire the 3 consuming files

**Files:**
- Create: `packages/platform-react/src/navigation/NavigationContext.ts`
- Modify: `packages/platform-react/src/index.ts`
- Modify: `packages/platform-react/src/api/ApiErrorMessage.tsx`
- Modify: `packages/platform-react/src/list/GeneratedList.tsx`
- Modify: `packages/platform-react/src/detail/RecordDetail.tsx`

**Interfaces:**
- Produces:
  ```ts
  export type NavigationAdapter = {
    toRecordList: (entityName: string) => string;
    toNewRecord: (entityName: string) => string;
    toRecordDetail: (entityName: string, id: string) => string;
    toEditRecord: (entityName: string, id: string) => string;
    toLogin: () => string;
    navigate: (path: string) => void;
    Link: ComponentType<{ to: string; children: ReactNode }>;
  };
  export const NavigationContext: Context<NavigationAdapter | null>;
  export function useNavigationAdapter(): NavigationAdapter;
  ```
  Task 2 (`apps/demo`) consumes `NavigationContext` and `NavigationAdapter` (both re-exported from `@metap/platform-react`'s barrel).

- [ ] **Step 1: Create the `NavigationContext` module**

Create `packages/platform-react/src/navigation/NavigationContext.ts`:

```ts
import { createContext, useContext } from "react";
import type { ComponentType, Context, ReactNode } from "react";

export type NavigationAdapter = {
  toRecordList: (entityName: string) => string;
  toNewRecord: (entityName: string) => string;
  toRecordDetail: (entityName: string, id: string) => string;
  toEditRecord: (entityName: string, id: string) => string;
  toLogin: () => string;
  navigate: (path: string) => void;
  Link: ComponentType<{ to: string; children: ReactNode }>;
};

export const NavigationContext: Context<NavigationAdapter | null> = createContext<NavigationAdapter | null>(null);

export function useNavigationAdapter(): NavigationAdapter {
  const adapter = useContext(NavigationContext);
  if (!adapter) {
    throw new Error(
      "useNavigationAdapter() called with no NavigationContext.Provider above it — every packages/platform-react consumer must provide one.",
    );
  }
  return adapter;
}
```

- [ ] **Step 2: Export it from the package barrel**

In `packages/platform-react/src/index.ts`, add a line (alphabetically after the `metadata/*` exports, before `workflow/WorkflowActionBar`):

```ts
export * from "./navigation/NavigationContext";
```

- [ ] **Step 3: Rewire `ApiErrorMessage.tsx`**

Replace the full contents of `packages/platform-react/src/api/ApiErrorMessage.tsx`:

```tsx
import { useNavigationAdapter } from "../navigation/NavigationContext";
import { ApiError } from "./client";

export function ApiErrorMessage({ error }: { error: unknown }) {
  const adapter = useNavigationAdapter();

  if (error instanceof ApiError && error.status === 401) {
    return (
      <div>
        Session expired. <adapter.Link to={adapter.toLogin()}>Sign in again</adapter.Link>.
      </div>
    );
  }

  return <div>Error: {error instanceof Error ? error.message : String(error)}</div>;
}
```

- [ ] **Step 4: Rewire `GeneratedList.tsx`**

In `packages/platform-react/src/list/GeneratedList.tsx`:

Remove the import `import { Link } from "react-router-dom";` (line 2) and add, alongside the other relative imports:

```tsx
import { useNavigationAdapter } from "../navigation/NavigationContext";
```

Inside the component body, right after `const { token } = useAuth();`, add:

```tsx
  const navAdapter = useNavigationAdapter();
```

Replace the "New" button:

```tsx
        <Button component={Link} to={`/records/${entityName}/new`}>
          New
        </Button>
```

with:

```tsx
        <Button component={navAdapter.Link} to={navAdapter.toNewRecord(entityName)}>
          New
        </Button>
```

Replace the per-row "View" link:

```tsx
                        <Anchor component={Link} to={`/records/${entityName}/${record.id}`}>
                          View
                        </Anchor>
```

with:

```tsx
                        <Anchor
                          component={navAdapter.Link}
                          to={navAdapter.toRecordDetail(entityName, record.id)}
                        >
                          View
                        </Anchor>
```

- [ ] **Step 5: Rewire `RecordDetail.tsx`**

In `packages/platform-react/src/detail/RecordDetail.tsx`:

Replace the import line `import { Link, useNavigate } from "react-router-dom";` with:

```tsx
import { useNavigationAdapter } from "../navigation/NavigationContext";
```

Replace `const navigate = useNavigate();` with:

```tsx
  const navAdapter = useNavigationAdapter();
```

In `handleDelete`, replace `navigate(\`/records/${entityName}\`);` with:

```tsx
      navAdapter.navigate(navAdapter.toRecordList(entityName));
```

Replace the "Edit" link:

```tsx
        <Anchor component={Link} to={`/records/${entityName}/${id}/edit`}>
          Edit
        </Anchor>
```

with:

```tsx
        <Anchor component={navAdapter.Link} to={navAdapter.toEditRecord(entityName, id)}>
          Edit
        </Anchor>
```

- [ ] **Step 6: Typecheck**

Run: `pnpm --filter @metap/platform-react exec tsc --noEmit`
Expected: fails at this point — `apps/demo` hasn't provided a `NavigationContext.Provider` yet, but `packages/platform-react` itself has no reason to fail; this step only checks `packages/platform-react`'s own types are internally consistent. If it fails for a reason other than a missing provider (impossible to detect at the type level — providers are a runtime concern), fix that reason before proceeding.
Expected: PASS (type-level, no runtime provider needed for `tsc` to succeed).

- [ ] **Step 7: Commit**

```bash
git add packages/platform-react/src/navigation/NavigationContext.ts \
  packages/platform-react/src/index.ts \
  packages/platform-react/src/api/ApiErrorMessage.tsx \
  packages/platform-react/src/list/GeneratedList.tsx \
  packages/platform-react/src/detail/RecordDetail.tsx
git commit -m "Decouple packages/platform-react from react-router-dom via NavigationAdapter"
```

---

### Task 2: `apps/demo`'s `react-router-dom`-backed adapter

**Files:**
- Create: `apps/demo/src/reactRouterNavigationAdapter.tsx`
- Modify: `apps/demo/src/main.tsx`

**Interfaces:**
- Consumes: `NavigationContext`, `NavigationAdapter` (from `@metap/platform-react`, Task 1).

- [ ] **Step 1: Write the adapter**

Create `apps/demo/src/reactRouterNavigationAdapter.tsx`:

```tsx
import type { ReactNode } from "react";
import { Link as RouterLink, useNavigate } from "react-router-dom";
import { NavigationContext } from "@metap/platform-react";
import type { NavigationAdapter } from "@metap/platform-react";

function useReactRouterNavigationAdapter(): NavigationAdapter {
  const navigate = useNavigate();

  return {
    toRecordList: (entityName) => `/records/${entityName}`,
    toNewRecord: (entityName) => `/records/${entityName}/new`,
    toRecordDetail: (entityName, id) => `/records/${entityName}/${id}`,
    toEditRecord: (entityName, id) => `/records/${entityName}/${id}/edit`,
    toLogin: () => "/dev-login",
    navigate,
    Link: RouterLink,
  };
}

export function ReactRouterNavigationProvider({ children }: { children: ReactNode }) {
  const adapter = useReactRouterNavigationAdapter();
  return <NavigationContext.Provider value={adapter}>{children}</NavigationContext.Provider>;
}
```

This encodes `apps/demo`'s own URL scheme (`/records/:entityName`, `/records/:entityName/new`, etc. — matching the routes already defined in `apps/demo/src/App.tsx`) and its own router choice (`react-router-dom`). A future consumer with a different scheme or router writes its own version of this one file.

- [ ] **Step 2: Wire it into `main.tsx`**

In `apps/demo/src/main.tsx`, add the import:

```tsx
import { ReactRouterNavigationProvider } from "./reactRouterNavigationAdapter";
```

Change the render tree from:

```tsx
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

to:

```tsx
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MantineProvider>
      <Notifications />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <ReactRouterNavigationProvider>
            <App />
          </ReactRouterNavigationProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
```

`ReactRouterNavigationProvider` must be *inside* `BrowserRouter` — it calls `useNavigate()` internally, which only works within a router's context.

- [ ] **Step 3: Typecheck, build, and lint**

Run: `pnpm typecheck` (repo root — recursive across every package)
Expected: no errors, including `packages/platform-react`'s own typecheck now that a real consumer exists.

Run: `pnpm --filter @metap/demo build`
Expected: production build succeeds.

Run: `pnpm lint` (repo root — recursive)
Expected: same baseline as before (the one pre-existing `AuthContext.tsx` fast-refresh warning, no new errors).

- [ ] **Step 4: Manual browser verification**

Start `docker compose up -d postgres rabbitmq` (if not already up), `pnpm dev` (API, repo root), `cd apps/demo && pnpm dev` (frontend, or `pnpm dev:web` from repo root). Log in via `/dev-login`, then confirm, on a `crm.customers` entity:
- The list page's "New" button navigates to the create form.
- A row's "View" link navigates to that record's detail page.
- The detail page's "Edit" link navigates to the edit form.
- Deleting a record from the detail page navigates back to the list.
- Triggering a `401` (e.g. an expired/invalid token) shows "Session expired. Sign in again." with a working link back to `/dev-login`.

This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries, no `sudo`, no cached alternative) — if that's still true, report it plainly rather than claiming visual verification succeeded; typecheck/build/lint are the actual verification available here.

- [ ] **Step 5: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing — this task touches no backend code.

- [ ] **Step 6: Commit**

```bash
git add apps/demo/src/reactRouterNavigationAdapter.tsx apps/demo/src/main.tsx
git commit -m "apps/demo: provide a react-router-dom-backed NavigationAdapter"
```
