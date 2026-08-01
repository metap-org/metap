# GeneratedList Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `GeneratedList`, a metadata-driven list component that renders columns/filters/sort for any entity from its metadata alone, and wire it in behind a generic `/records/:entityName` route — replacing Slice #1's hardcoded `CustomersPage`.

**Architecture:** A new `useEntity(entityName)` hook fetches one entity's full definition. `GeneratedList` reads that entity's `listViews[0]` (columns, filterable fields, max limit) and `fields` (labels, `sortable`, `kind`, `enumValues`) to render a Mantine `Table` with clickable sortable headers and per-column filter inputs (a `Select` for `kind: "enum"` fields, a debounced text input otherwise), fetching through the existing `useApiQuery` hook with a dynamically built query string.

**Tech Stack:** React, Mantine (`Table`, `Select`, `TextInput`, `@mantine/hooks`'s `useDebouncedValue`), TanStack Query (via the existing `useApiQuery`), React Router (`useParams`).

## Global Constraints

- `GeneratedList` must work for any entity name passed to it — no `crm.customers`-specific logic anywhere in the component. It's a `web/src/platform/` file.
- No pagination UI — fetch up to `listView.maxLimit` and render what comes back, nothing more. Don't fake client-side pagination.
- Filter values are only sent to the backend once non-empty (trimmed) — an empty filter input means "no filter," not `?field=`.
- Text filter inputs are debounced (~400ms) before triggering a refetch; the enum `Select` refetches immediately (it's a discrete choice, not typed text).
- Loading, error, and empty states must all be visually distinguishable — this closes a gap flagged in the prior slice's review.
- `web/src/demo/CustomersPage.tsx` is deleted outright once `/records/:entityName` replaces its purpose — don't leave it around unused.
- No automated tests — matches the prior slice's convention (still scaffolding/tooling, not business logic with locked-in edge cases). Verify manually.
- Resolved dependency versions from prior slices: Mantine 9.5.0, React 19.2.7, TanStack Query 5.101.4, React Router 7.18.1, `@mantine/hooks` 9.5.0 (already a dependency, unused so far — this plan uses its `useDebouncedValue`). Don't assume any specific Mantine `Select`/`Table` prop shape is correct without checking — this codebase has twice needed to adjust component usage slightly to match the actually-installed v9 API; verify via a real `pnpm build` and treat TypeScript errors as the real API, not the plan's snippets, as truth.
- Two pre-existing, unrelated backend typecheck errors (`src/infra/messaging/rabbitmq.ts`) predate this plan and are irrelevant.

---

### Task 1: Shared metadata types + `useEntity` hook

**Files:**
- Create: `web/src/platform/metadata/types.ts`
- Modify: `web/src/platform/metadata/useEntities.ts`
- Create: `web/src/platform/metadata/useEntity.ts`

**Interfaces:**
- Produces: `EntityField = { name: string; label: string; kind: string; required?: boolean; searchable?: boolean; sortable?: boolean; enumValues?: readonly string[] }`, `EntityListView = { name: string; label: string; fields: readonly string[]; filters: readonly string[]; defaultSort?: string; maxLimit: number }`, `EntitySummary = { name: string; label: string; fields: readonly EntityField[]; listViews: readonly EntityListView[]; workflow?: unknown }` — all from `web/src/platform/metadata/types.ts`. `useEntity(entityName: string)` — a TanStack Query result (via `useApiQuery`) whose `data` is `EntitySummary | undefined`.
- Consumes: `useApiQuery` (existing, `web/src/platform/api/useApiQuery.ts`).

- [ ] **Step 1: Create `web/src/platform/metadata/types.ts`**

```ts
export type EntityField = {
  name: string;
  label: string;
  kind: string;
  required?: boolean;
  searchable?: boolean;
  sortable?: boolean;
  enumValues?: readonly string[];
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
```

This is the same shape `useEntities.ts` already declares inline, plus the new `enumValues` field on `EntityField` — the backend's `EntityField` (`src/core/metadata/entity.ts`) always includes `enumValues` for `kind: "enum"` fields (see `src/modules/crm/customer.entity.ts`'s `status` field), but the frontend's copy never declared it. `GeneratedList` (Task 2) needs it for the enum-field filter dropdown.

- [ ] **Step 2: Update `web/src/platform/metadata/useEntities.ts` to import the shared types**

Replace the file's content with:

```ts
import { useApiQuery } from "../api/useApiQuery";
import type { EntitySummary } from "./types";

export function useEntities() {
  return useApiQuery<{ data: EntitySummary[] }, EntitySummary[]>(
    ["entities"],
    "/metadata/entities",
    (response) => response.data,
  );
}
```

(The inline `EntityField`/`EntityListView`/`EntitySummary` type declarations move to `types.ts` from Step 1 and are removed from this file. `web/src/demo/EntitiesPage.tsx` only imports the `useEntities` function from this file, not the types, so this is not a breaking change for it.)

- [ ] **Step 3: Create `web/src/platform/metadata/useEntity.ts`**

```ts
import { useApiQuery } from "../api/useApiQuery";
import type { EntitySummary } from "./types";

export function useEntity(entityName: string) {
  return useApiQuery<{ data: EntitySummary }, EntitySummary>(
    ["entity", entityName],
    `/metadata/entities/${entityName}`,
    (response) => response.data,
  );
}
```

This wraps `GET /metadata/entities/:entity` — an existing backend route (`src/server/routes/metadata.ts`) that returns `{ data: entity }` for one entity, already built but not yet used by the frontend.

- [ ] **Step 4: Verify**

Run: `pnpm build` (inside `web/`)
Expected: no TypeScript errors.

- [ ] **Step 5: Leave uncommitted**

Per this project's standing rule, do not run `git commit`.

---

### Task 2: `GeneratedList` component

**Files:**
- Create: `web/src/platform/list/GeneratedList.tsx`

**Interfaces:**
- Consumes: `useEntity` (Task 1), `useApiQuery` (existing), `ApiErrorMessage` (existing, `web/src/platform/api/ApiErrorMessage.tsx`), `EntityField`/`EntityListView` types (Task 1, `web/src/platform/metadata/types.ts`).
- Produces: `<GeneratedList entityName={string} />` — a self-contained component, no other exports needed.

- [ ] **Step 1: Write `web/src/platform/list/GeneratedList.tsx`**

```tsx
import { useMemo, useState } from "react";
import { useDebouncedValue } from "@mantine/hooks";
import { Container, Select, Table, TextInput, Title } from "@mantine/core";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntity } from "../metadata/useEntity";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

type SortState = { field: string; descending: boolean } | null;

export function GeneratedList({ entityName }: { entityName: string }) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  const [filterInputs, setFilterInputs] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<SortState>(null);
  const [debouncedFilters] = useDebouncedValue(filterInputs, 400);

  const listView = entity?.listViews[0];
  const fieldsByName = useMemo(
    () => new Map((entity?.fields ?? []).map((field) => [field.name, field])),
    [entity],
  );

  const activeFilters = useMemo(() => {
    const result: Record<string, string> = {};
    for (const [key, value] of Object.entries(debouncedFilters)) {
      if (value.trim().length > 0) {
        result[key] = value.trim();
      }
    }
    return result;
  }, [debouncedFilters]);

  const queryParams = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", String(listView?.maxLimit ?? 30));
    if (sort) {
      params.set("sort", sort.descending ? `-${sort.field}` : sort.field);
    }
    for (const [key, value] of Object.entries(activeFilters)) {
      params.set(key, value);
    }
    return params.toString();
  }, [listView, sort, activeFilters]);

  const {
    data: records,
    isLoading: recordsLoading,
    error: recordsError,
  } = useApiQuery<{ data: RecordDto[] }, RecordDto[]>(
    ["records", entityName, sort, activeFilters],
    `/api/${entityName}?${queryParams}`,
    (response) => response.data,
  );

  if (entityLoading) {
    return <div>Loading...</div>;
  }

  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }

  if (!entity || !listView) {
    return <div>Entity not found.</div>;
  }

  function toggleSort(field: EntityField) {
    if (!field.sortable) {
      return;
    }

    setSort((current) => {
      if (!current || current.field !== field.name) {
        return { field: field.name, descending: false };
      }

      if (!current.descending) {
        return { field: field.name, descending: true };
      }

      return null;
    });
  }

  const columnCount = listView.fields.length;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {entity.label}
      </Title>
      <Table>
        <Table.Thead>
          <Table.Tr>
            {listView.fields.map((fieldName) => {
              const field = fieldsByName.get(fieldName);

              if (!field) {
                return <Table.Th key={fieldName} />;
              }

              return (
                <Table.Th
                  key={fieldName}
                  onClick={() => toggleSort(field)}
                  style={{ cursor: field.sortable ? "pointer" : undefined }}
                >
                  {field.label}
                  {sort?.field === fieldName ? (sort.descending ? " ▼" : " ▲") : ""}
                </Table.Th>
              );
            })}
          </Table.Tr>
          <Table.Tr>
            {listView.fields.map((fieldName) => {
              if (!listView.filters.includes(fieldName)) {
                return <Table.Th key={fieldName} />;
              }

              const field = fieldsByName.get(fieldName);

              if (field?.kind === "enum") {
                return (
                  <Table.Th key={fieldName}>
                    <Select
                      placeholder="Any"
                      clearable
                      data={(field.enumValues ?? []).map((value) => ({ value, label: value }))}
                      value={filterInputs[fieldName] ?? null}
                      onChange={(value) =>
                        setFilterInputs((prev) => ({ ...prev, [fieldName]: value ?? "" }))
                      }
                    />
                  </Table.Th>
                );
              }

              return (
                <Table.Th key={fieldName}>
                  <TextInput
                    placeholder="Filter..."
                    value={filterInputs[fieldName] ?? ""}
                    onChange={(event) =>
                      setFilterInputs((prev) => ({
                        ...prev,
                        [fieldName]: event.currentTarget.value,
                      }))
                    }
                  />
                </Table.Th>
              );
            })}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {recordsLoading ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>Loading...</Table.Td>
            </Table.Tr>
          ) : recordsError ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>
                <ApiErrorMessage error={recordsError} />
              </Table.Td>
            </Table.Tr>
          ) : records && records.length === 0 ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>No records.</Table.Td>
            </Table.Tr>
          ) : (
            records?.map((record) => (
              <Table.Tr key={record.id}>
                {listView.fields.map((fieldName) => (
                  <Table.Td key={fieldName}>{String(record.data[fieldName] ?? "")}</Table.Td>
                ))}
              </Table.Tr>
            ))
          )}
        </Table.Tbody>
      </Table>
    </Container>
  );
}
```

Notes for whoever implements this:
- `record.data[fieldName]` works uniformly for every column, including `code`/`status` — the backend always stores the full validated entity payload (including those fields) inside the `data` JSONB blob in addition to mirroring `code`/`status` onto their own top-level columns (see `CrudService.create`/`update` in the backend, `src/core/crud/crud-service.ts`). No special-casing needed.
- If `@mantine/hooks`'s `useDebouncedValue` has a different return shape than `[value]` (e.g. it might return `[value, { cancel, flush }]` or similar depending on the exact version) — adjust the destructuring to match; the intent is "a debounced copy of `filterInputs`, delayed 400ms."
- If Mantine 9's `Select`/`TextInput`/`Table.Th` prop names or event shapes differ from what's written here, adjust to match the real installed API (same instruction prior tasks in this project have already followed successfully) — verify via `pnpm build` and real TypeScript errors, not by guessing.

- [ ] **Step 2: Verify it typechecks**

Run: `pnpm build` (inside `web/`)
Expected: no TypeScript errors. This component isn't wired into any route yet (that's Task 3), so this step only confirms it compiles in isolation.

- [ ] **Step 3: Leave uncommitted**

Do not commit.

---

### Task 3: Generic routing, cleanup, and end-to-end verification

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/demo/EntitiesPage.tsx`
- Delete: `web/src/demo/CustomersPage.tsx`

**Interfaces:**
- Consumes: `GeneratedList` (Task 2).

- [ ] **Step 1: Add the generic route and remove the `CustomersPage` route, in `web/src/App.tsx`**

Replace the file's content with:

```tsx
import type { ReactNode } from "react";
import { Navigate, Route, Routes, useParams } from "react-router-dom";
import { AuthProvider, useAuth } from "./platform/auth/AuthContext";
import { GeneratedList } from "./platform/list/GeneratedList";
import { DevLoginPage } from "./demo/DevLoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/dev-login" replace />;
  }

  return <>{children}</>;
}

function RecordsRoute() {
  const { entityName } = useParams<{ entityName: string }>();

  if (!entityName) {
    return <div>Missing entity name.</div>;
  }

  return <GeneratedList entityName={entityName} />;
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
          path="/records/:entityName"
          element={
            <RequireAuth>
              <RecordsRoute />
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
```

- [ ] **Step 2: Fix the hardcoded link in `web/src/demo/EntitiesPage.tsx`**

Change:
```tsx
            <Anchor component={Link} to="/customers">
```
to:
```tsx
            <Anchor component={Link} to={`/records/${entity.name}`}>
```

- [ ] **Step 3: Delete `web/src/demo/CustomersPage.tsx`**

It's fully replaced by `GeneratedList` reached via the generic route. Confirm nothing else imports it (`grep -rn CustomersPage web/src` should return nothing after this).

- [ ] **Step 4: Typecheck**

Run: `pnpm build` (inside `web/`)
Expected: no TypeScript errors.

- [ ] **Step 5: End-to-end manual verification**

Bring up the backend if not already running (`docker compose up -d postgres rabbitmq` + `pnpm db:migrate` + `pnpm dev`, repo root) and the frontend (`pnpm dev`, inside `web/`). Mint a token (`pnpm mint-token`, repo root).

Create a few records to have something to filter/sort against:
```bash
TOKEN="<paste minted token>"
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"GL1","name":"Acme Corp","status":"active"}}' \
  http://localhost:3000/api/crm.customers
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"GL2","name":"Acme Industries","status":"draft"}}' \
  http://localhost:3000/api/crm.customers
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"GL3","name":"Beta LLC","status":"active"}}' \
  http://localhost:3000/api/crm.customers
```

Using a real browser if available (per this project's established convention — if not, do the equivalent checks by hitting the same URLs the frontend would call directly via `curl` through the Vite proxy, e.g. `curl -H "Authorization: Bearer $TOKEN" "http://localhost:5173/api/crm.customers?status=active&limit=30"`, and say clearly in your report that the actual rendering wasn't visually confirmed):

1. Paste the token at `/dev-login`, land on `/` — the entities page.
2. Click "Customer (crm.customers)" — should navigate to `/records/crm.customers` and show a table with columns Code, Name, Phone, Email, Status, all 3 rows visible.
3. Type "Acme" into the Name filter — after ~400ms, the table should narrow to 2 rows (GL1, GL2).
4. Clear the Name filter, select "active" in the Status filter dropdown — table should narrow to 2 rows (GL1, GL3).
5. Clear the Status filter, click the "Code" column header — rows should sort ascending by code; click again — descending.
6. Clear all filters and set a filter value that matches nothing (e.g. type "zzz" into Name) — the table should show "No records." (not a blank table, not an error).
7. Navigate to `/records/does-not-exist` directly — should show "Entity not found." rather than crashing.

Clean up the 3 test records:
```bash
docker compose exec -T postgres psql -U metap -d metap -c "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM records WHERE code IN ('GL1','GL2','GL3')); DELETE FROM records WHERE code IN ('GL1','GL2','GL3');"
```

Stop both dev servers when done.

- [ ] **Step 6: Leave uncommitted**

Do not commit. The user reviews and commits everything from this whole plan together.
