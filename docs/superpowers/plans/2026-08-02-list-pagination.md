# List Pagination + Virtualization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `GeneratedList` fetches and renders every page of a keyset-paginated entity list, not just the first, using infinite-scroll accumulation and windowed (virtualized) row rendering so the DOM never holds more rows than are actually visible.

**Architecture:** Frontend-only — the backend's keyset pagination (`GET /api/:entity?limit=N&cursor=...` → `{ data, page: { limit, nextCursor } }`) already exists from Phase 4. A new `useApiInfiniteQuery` hook (mirroring the existing `useApiQuery`) wraps TanStack Query's `useInfiniteQuery` to accumulate pages keyed by cursor. `GeneratedList` flattens the accumulated pages into one array and windows it with `@tanstack/react-virtual`, fetching the next page when the virtualizer's rendered range approaches the end of what's loaded.

**Tech Stack:** React 19, Mantine, TanStack Query (already installed), `@tanstack/react-virtual` (new dependency, same vendor as TanStack Query). `web/` has no test framework — verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check.

## Global Constraints

- No backend changes — this sub-project only touches `web/`.
- Only `@tanstack/react-virtual` is a new dependency; no other new package.
- A filter or sort change must produce a fresh page list (not append to the old one) — achieved by including `sort`/`activeFilters` in the query key, exactly as `GeneratedList` already does today.
- Only the table body is virtualized; the header row and filter row render normally (sticky-positioned so they stay visible while the body scrolls).
- `web/` has no test framework — verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check. This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries, no `sudo`, no cached alternative) — if that's still true, report it plainly rather than claiming visual verification succeeded.

---

### Task 1: `useApiInfiniteQuery` hook

**Files:**
- Create: `web/src/platform/api/useApiInfiniteQuery.ts`

**Interfaces:**
- Produces:
  ```ts
  export function useApiInfiniteQuery<TFetched>(
    queryKey: QueryKey,
    buildPath: (cursor: string | null) => string,
    getNextCursor: (lastPage: TFetched) => string | null,
    enabled?: boolean,
  ): UseInfiniteQueryResult<InfiniteData<TFetched, string | null>>;
  ```
  Task 2 (`GeneratedList`) consumes this, passing `TFetched = { data: RecordDto[]; page: { limit: number; nextCursor: string | null } }`.

- [ ] **Step 1: Write the hook**

Create `web/src/platform/api/useApiInfiniteQuery.ts`:

```ts
import { useInfiniteQuery } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch } from "./client";

export function useApiInfiniteQuery<TFetched>(
  queryKey: QueryKey,
  buildPath: (cursor: string | null) => string,
  getNextCursor: (lastPage: TFetched) => string | null,
  enabled: boolean = true,
) {
  const { token } = useAuth();

  return useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) => apiFetch<TFetched>(buildPath(pageParam), token),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => getNextCursor(lastPage),
    enabled: token !== null && enabled,
  });
}
```

This mirrors the existing `web/src/platform/api/useApiQuery.ts` — same `token`/`enabled` handling, same `apiFetch` call — just wrapping `useInfiniteQuery` instead of `useQuery`, with `buildPath`/`getNextCursor` as the two pieces that vary per caller (path construction needs the cursor; cursor extraction needs to know where it lives in the response shape).

- [ ] **Step 2: Typecheck**

Run: `cd web && pnpm build`
Expected: no errors. (No caller exists yet, so this only checks the hook's own types are internally consistent — TanStack Query's generics will fully resolve once Task 2 calls it with a concrete `TFetched`.)

- [ ] **Step 3: Commit**

```bash
git add web/src/platform/api/useApiInfiniteQuery.ts
git commit -m "Add useApiInfiniteQuery: cursor-paginated TanStack Query wrapper"
```

---

### Task 2: `@tanstack/react-virtual` + `GeneratedList` rewrite

**Files:**
- Modify: `web/package.json` / `web/pnpm-lock.yaml` (new dependency)
- Modify: `web/src/platform/list/GeneratedList.tsx`

**Interfaces:**
- Consumes: `useApiInfiniteQuery` (Task 1).

- [ ] **Step 1: Add the dependency**

Run: `cd web && pnpm add @tanstack/react-virtual`
Expected: adds the package to `web/package.json`'s `dependencies` and updates `web/pnpm-lock.yaml`.

- [ ] **Step 2: Rewrite `GeneratedList.tsx`**

Replace the full contents of `web/src/platform/list/GeneratedList.tsx`:

```tsx
import { useEffect, useMemo, useRef, useState } from "react";
import { useDebouncedValue } from "@mantine/hooks";
import { useVirtualizer } from "@tanstack/react-virtual";
import { Container, Select, Table, TextInput, Title } from "@mantine/core";
import { useApiInfiniteQuery } from "../api/useApiInfiniteQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { FieldValue } from "../field/FieldValue";
import { useEntity } from "../metadata/useEntity";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

type ListPage = {
  data: RecordDto[];
  page: { limit: number; nextCursor: string | null };
};

type SortState = { field: string; descending: boolean } | null;

const ROW_HEIGHT = 40;

export function GeneratedList({ entityName }: { entityName: string }) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  // Text filters are debounced (wait for the user to stop typing before refetching).
  const [filterInputs, setFilterInputs] = useState<Record<string, string>>({});
  // Enum filters come from a Select, not free text, so they refetch immediately on change.
  const [enumFilters, setEnumFilters] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<SortState>(null);
  const [debouncedTextFilters] = useDebouncedValue(filterInputs, 400);

  const listView = entity?.listViews[0];
  const fieldsByName = useMemo(
    () => new Map((entity?.fields ?? []).map((field) => [field.name, field])),
    [entity],
  );

  const activeFilters = useMemo(() => {
    const result: Record<string, string> = {};
    for (const [key, value] of Object.entries(debouncedTextFilters)) {
      if (value.trim().length > 0) {
        result[key] = value.trim();
      }
    }
    for (const [key, value] of Object.entries(enumFilters)) {
      if (value.trim().length > 0) {
        result[key] = value.trim();
      }
    }
    return result;
  }, [debouncedTextFilters, enumFilters]);

  const baseParams = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", String(listView?.maxLimit ?? 30));
    if (sort) {
      params.set("sort", sort.descending ? `-${sort.field}` : sort.field);
    }
    for (const [key, value] of Object.entries(activeFilters)) {
      params.set(key, value);
    }
    return params;
  }, [listView, sort, activeFilters]);

  const {
    data,
    isLoading: recordsLoading,
    error: recordsError,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useApiInfiniteQuery<ListPage>(
    ["records", entityName, sort, activeFilters],
    (cursor) => {
      const params = new URLSearchParams(baseParams);
      if (cursor) {
        params.set("cursor", cursor);
      }
      return `/api/${entityName}?${params.toString()}`;
    },
    (lastPage) => lastPage.page.nextCursor,
    Boolean(entity && listView),
  );

  const records = useMemo(() => data?.pages.flatMap((page) => page.data) ?? [], [data]);

  const scrollContainerRef = useRef<HTMLDivElement>(null);

  const rowVirtualizer = useVirtualizer({
    count: records.length,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  const virtualRows = rowVirtualizer.getVirtualItems();
  const lastVirtualIndex = virtualRows[virtualRows.length - 1]?.index;

  useEffect(() => {
    if (lastVirtualIndex === undefined) {
      return;
    }
    if (lastVirtualIndex >= records.length - 10 && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [lastVirtualIndex, records.length, hasNextPage, isFetchingNextPage, fetchNextPage]);

  if (entityLoading) {
    return <div>Loading...</div>;
  }

  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }

  if (!entity) {
    return <div>Entity not found.</div>;
  }

  if (!listView) {
    return <div>{entity.label} has no list view configured.</div>;
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
      <div ref={scrollContainerRef} style={{ height: 600, overflow: "auto" }}>
        <Table>
          <Table.Thead
            style={{ position: "sticky", top: 0, zIndex: 1, background: "var(--mantine-color-body)" }}
          >
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
                        value={enumFilters[fieldName] || null}
                        onChange={(value) =>
                          setEnumFilters((prev) => ({ ...prev, [fieldName]: value ?? "" }))
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
                      onChange={(event) => {
                        const value = event.currentTarget.value;
                        setFilterInputs((prev) => ({ ...prev, [fieldName]: value }));
                      }}
                    />
                  </Table.Th>
                );
              })}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody style={{ position: "relative", height: rowVirtualizer.getTotalSize() }}>
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
            ) : records.length === 0 ? (
              <Table.Tr>
                <Table.Td colSpan={columnCount}>No records.</Table.Td>
              </Table.Tr>
            ) : (
              virtualRows.map((virtualRow) => {
                const record = records[virtualRow.index];

                if (!record) {
                  return null;
                }

                return (
                  <Table.Tr
                    key={record.id}
                    style={{
                      position: "absolute",
                      transform: `translateY(${virtualRow.start}px)`,
                      width: "100%",
                    }}
                  >
                    {listView.fields.map((fieldName) => {
                      const field = fieldsByName.get(fieldName);

                      return (
                        <Table.Td key={fieldName}>
                          {field ? <FieldValue field={field} value={record.data[fieldName]} /> : null}
                        </Table.Td>
                      );
                    })}
                  </Table.Tr>
                );
              })
            )}
          </Table.Tbody>
        </Table>
        {isFetchingNextPage ? (
          <div style={{ padding: 8, textAlign: "center" }}>Loading more…</div>
        ) : null}
      </div>
    </Container>
  );
}
```

Notes on what changed and why:
- `records` is now `data?.pages.flatMap(...)` instead of a single page's array — the accumulation TanStack Query's `useInfiniteQuery` already manages, not custom state.
- The scroll-triggered fetch effect keys off `lastVirtualIndex` (a primitive extracted from `virtualRows`, not the array itself) so it doesn't re-fire on every render where the array reference changes but the last rendered index doesn't.
- Rows are keyed by `record.id`, not by virtual index — the index shifts as the window scrolls, but the id is the stable identity of the actual data.
- The loading/error/empty branches render one plain (non-absolutely-positioned) row directly inside `Table.Tbody`, same as before — only the "real data" branch uses the virtualizer's absolute positioning.

- [ ] **Step 3: Typecheck, build, and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Manual browser verification**

Start `docker compose up -d postgres rabbitmq` (if not already up), `pnpm dev` (API, repo root), `cd web && pnpm dev` (frontend). Log in via `/dev-login`. Create more `crm.customers` records than one page holds — either through the UI repeatedly, or via a short loop of `curl -X POST` calls against `/api/crm.customers` with the minted token — enough to exceed `maxLimit` (100) so a second page exists. Navigate to `/records/crm.customers` and confirm:
- Scrolling the table body loads additional pages automatically (a brief "Loading more…" indicator appears near the bottom while it does).
- The header row and filter row stay visible (sticky) while the body scrolls underneath them.
- Changing a filter or the sort order resets the visible list back to a fresh first page, not an accumulation of both old and new results.
- Row content (badges, masked-field indicators, em-dashes) still renders correctly — this is the same `FieldValue` component from sub-project 1/4, now just windowed.

This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries, no `sudo`, no cached alternative) — if that's still true, report it plainly rather than claiming visual verification succeeded; typecheck/lint are the actual verification available here.

- [ ] **Step 5: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing — this task touches no backend code.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/pnpm-lock.yaml web/src/platform/list/GeneratedList.tsx
git commit -m "GeneratedList: infinite-scroll pagination with virtualized row rendering"
```
