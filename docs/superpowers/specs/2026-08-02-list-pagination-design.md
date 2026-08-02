# List Pagination + Virtualization for GeneratedList

Date: 2026-08-02

Status: approved

Scope: fifth and last of the planned Phase 6 (Frontend Core) sub-projects, in this priority order:

1. FieldRenderer foundation (`FieldValue`) — done
2. `GeneratedForm` — done
3. `WorkflowActionBar` — done
4. Permission-aware UI state — done
5. **Pagination / virtualization for `GeneratedList`** (this spec)

Sub-projects 1-4: `docs/superpowers/specs/2026-08-02-{field-renderer,generated-form,workflow-action-bar,permission-aware-ui}-design.md`, all implemented this session, uncommitted, pending review.

## Motivation

`GeneratedList` (`web/src/platform/list/GeneratedList.tsx`) currently fetches exactly one page via `useApiQuery`, capped at the entity's `listView.maxLimit` (100 for `crm.customers`), and never sends a `cursor`. There's no way to see a record past the first page — the backend's keyset pagination (`GET /api/:entity?limit=N&cursor=...` → `{ data, page: { limit, nextCursor } }`, forward-only, opaque cursor, `nextCursor: null` when exhausted, added in Phase 4) is entirely unused by the frontend.

## Design

### Data fetching: `useApiInfiniteQuery`

New hook, `web/src/platform/api/useApiInfiniteQuery.ts`, mirroring the existing `useApiQuery` but wrapping TanStack Query's `useInfiniteQuery` instead of `useQuery`:

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
    queryFn: ({ pageParam }) => apiFetch<TFetched>(buildPath(pageParam as string | null), token),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => getNextCursor(lastPage),
    enabled: token !== null && enabled,
  });
}
```

`GeneratedList` calls it with the same `queryKey` shape it uses today (`["records", entityName, sort, activeFilters]`) — changing a filter or the sort order produces a new key, which TanStack Query treats as a brand-new query with its own empty page list, so filter/sort changes naturally discard all previously-accumulated pages rather than needing manual reset logic. `buildPath` appends `cursor` to the existing query-param builder only when a cursor is passed (first page has none). `getNextCursor` reads `page.nextCursor` off the fetched page.

`records` becomes `data?.pages.flatMap((p) => p.data) ?? []`, memoized.

### Virtualized rendering

New dependency: `@tanstack/react-virtual` (same vendor as the already-installed `@tanstack/react-query`; a small, focused, actively-maintained package — writing manual scroll-windowing math from scratch would be a strictly worse, higher-risk version of what this already does well).

`useVirtualizer` windows the flattened `records` array with a fixed row-height estimate (`estimateSize: () => 40`, matching Mantine's default table row height). Only the table **body** is virtualized — the header row and the filter row stay outside the virtualizer, rendered exactly as today, with `position: sticky; top: 0` so they remain visible while the body scrolls underneath.

Structure (the standard TanStack-virtual table recipe): a fixed-height (`height: 600`, `overflow: auto`) scroll container `<div>` wraps the `<Table>`; `<Table.Tbody>` gets `position: relative` and an explicit `height` equal to `rowVirtualizer.getTotalSize()`; each rendered `<Table.Tr>` gets `position: absolute`, `transform: translateY(${virtualRow.start}px)`, `width: 100%`, keyed by the record's `id` (not the virtual index, so React doesn't misattribute state across re-windowing).

### Fetching more pages

A `useEffect` watching `rowVirtualizer.getVirtualItems()`: when the last rendered virtual row's index comes within 10 rows of the end of the currently-loaded `records` array, and `hasNextPage && !isFetchingNextPage`, call `fetchNextPage()`. A small "Loading more…" row renders at the bottom of the table while `isFetchingNextPage` is true (outside the virtualized range, appended after it).

### Loading, empty, and error states

Unchanged in spirit from today: `entityLoading`/`entityError` gate the whole page as before. For the records themselves: first-page loading (`isLoading` from `useInfiniteQuery`, true only before any page has loaded) shows the existing "Loading..." row; `error` shows `ApiErrorMessage` as before; zero total records after the first page loads shows "No records." as before. None of this changes — only what happens *after* the first page (more pages, windowed rendering) is new.

## Out of scope (deliberate, not an oversight)

- **Scroll-to-top on filter/sort change.** The query-key change already empties `records` back to `[]` while the new first page loads, which visually resets the view without needing explicit scroll-position management.
- **Configurable/dynamic row height.** A fixed 40px estimate is accurate enough for this table's actual content (short text/badges/em-dashes, no wrapping multi-line cells) — dynamic measurement (`measureElement`) would add real complexity for a case that doesn't need it.
- **Virtualizing the header or filter row.** Never more than a handful of columns; nothing to window there.
- **Raising `listView.maxLimit` per entity.** A separate, per-entity metadata decision, not part of this sub-project (confirmed with the user during brainstorming) — this sub-project ships virtualization as infrastructure regardless of today's row counts.
- **A `Previous` control.** Keyset cursors are forward-only; going back would require the frontend to maintain its own cursor-history stack, which infinite-scroll-style accumulation (the chosen UX) doesn't need — there's nothing to page "back" to since everything already fetched stays rendered above the current scroll position.

## Testing

`web/` has no test framework — verification is `tsc -b`/`pnpm build` + `oxlint` + a manual browser check (create enough `crm.customers` records via the API to exceed one page, scroll the list, confirm more rows load and render correctly, confirm a filter/sort change resets the accumulated list). This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries, no `sudo`, no cached alternative) — if that's still true when this is implemented, that limitation gets reported plainly rather than claiming visual verification succeeded, same as sub-projects 1-4.

This is the last Phase 6 sub-project — once its plan is implemented and verified, a single consolidated verification pass covering both this sub-project and sub-project 4 (Permission-aware UI state) should follow, per the earlier agreement to verify once at the end rather than after each of the last two sub-projects individually.
