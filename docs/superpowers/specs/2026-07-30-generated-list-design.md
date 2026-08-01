# Frontend Slice #2: `GeneratedList`

Date: 2026-07-30

Status: approved

Scope: second frontend slice, building on Slice #1 (scaffold + dev-login + api/metadata client). This is the first genuinely metadata-driven platform component — it must work for any entity from metadata alone, not just `crm.customers`, even though `crm.customers` is still the only registered entity to prove it against. `GeneratedForm` and `WorkflowActionBar` remain separate, later slices.

## Motivation

Slice #1's `CustomersPage` was a hardcoded, entity-specific page — it proved the pipeline (auth → api-client → real backend) works, but nothing about it was "generated." This slice replaces it with a real `GeneratedList` component driven entirely by entity metadata (`fields`, `listViews`), exercising the backend's already-built filter/sort capability instead of ignoring it.

## Design

### Shared metadata types (`web/src/platform/metadata/types.ts`)

`EntityField`, `EntityListView`, `EntitySummary` move out of `useEntities.ts` into a shared file, since a new `useEntity` hook needs the same shapes. `EntityField` gains `enumValues?: readonly string[]` — currently missing from the frontend's type even though the backend always has it for `kind: "enum"` fields; this was flagged in the prior slice's review as a real gap, and `GeneratedList`'s enum-field filter needs it.

### `useEntity(entityName)` (`web/src/platform/metadata/useEntity.ts`)

Wraps `GET /metadata/entities/:entity` (the single-entity backend route, already built, currently unused by the frontend) via `useApiQuery`, returning one `EntitySummary`. `GeneratedList` needs exactly one entity's definition, not the full list `useEntities` already serves.

### `GeneratedList` (`web/src/platform/list/GeneratedList.tsx`)

```tsx
<GeneratedList entityName="crm.customers" />
```

- **Columns:** one per name in `entity.listViews[0].fields`; header text from the matching `EntityField.label`.
- **Sort:** a column is clickable only if its `EntityField.sortable === true`. Clicking toggles ascending → descending → (back to ascending); local component state tracks the current `{ field, descending }` or none, translated into the `sort` query param (`field` / `-field`) the backend already understands.
- **Filters:** one input per name in `entity.listViews[0].filters`. If the matching `EntityField.kind === "enum"`, render a Mantine `Select` (clearable) with `enumValues` as options. Otherwise, a plain text input. Text inputs are debounced (~400ms) before triggering a refetch; the `Select` refetches immediately on change (a discrete choice, not typed text — no debounce needed). Only filters with a non-empty value are included in the request.
- **Fetching:** builds `/api/${entityName}?limit=<maxLimit>&sort=<...>&<filter params>` and calls the existing `useApiQuery(["records", entityName, sort, filters], path, (r) => r.data)` — no changes needed to `useApiQuery` itself.
- **No pagination UI.** The backend has no real pagination (Slice #1 removed the dead `cursor` field); `GeneratedList` just fetches up to `entity.listViews[0].maxLimit` and shows what comes back. Real pagination is a future slice, gated on the backend actually building keyset pagination (roadmap Phase 4) — not something to fake on the frontend now.
- **States:** loading, error (via the existing `ApiErrorMessage`), and an explicit empty state ("No records" or similar) — all three visually distinguishable, closing a gap flagged in Slice #1's review (an empty successful response and a rendering failure looked identical before).
- **Row data access:** each row's underlying record is `{ id, code, status, version, data: Record<string, unknown>, ... }` (the same shape `/api/:entity` always returns); cell values for non-system columns come from `record.data[fieldName]`. `id`/`version` stay available on each row's underlying data even though nothing in this slice uses them yet — `GeneratedForm` (a later slice) will need both for editing.

### Routing becomes entity-generic

- `App.tsx` gains a `/records/:entityName` route rendering `<GeneratedList entityName={entityName} />` (reading the param via React Router). This replaces the entity-specific `/customers` route.
- `EntitiesPage.tsx`'s links change from the hardcoded `to="/customers"` to `to={`/records/${entity.name}`}` — another gap flagged in Slice #1's review (every entity linked to the same page regardless of which one), now genuinely fixed rather than deferred again.
- `web/src/demo/CustomersPage.tsx` is deleted outright — `GeneratedList` fully replaces its purpose, and keeping both would just be duplicated, drifting logic.

## Out of scope (this slice)

- Create/edit forms (`GeneratedForm`, a separate later slice).
- Any per-row "Edit" action or entry point into a form — `GeneratedForm`'s slice adds that.
- Real (keyset) pagination — backend doesn't have it yet (roadmap Phase 4).
- Field-kind-aware rendering beyond string vs. enum for filters (e.g. date-range filters, number filters) — every field on `crm.customers` besides `status` is a plain string today; deeper kind-awareness is worth building once an entity actually has a `date`/`number`/`money` field to prove it against.

## Testing

No automated tests — consistent with Slice #1's convention (this is still frontend scaffolding/tooling, not business logic with edge cases worth locking in yet). Verified manually: real backend, real browser, filtering by the enum field and a text field, sorting a sortable column in both directions, confirming the empty/loading/error states are each visually distinct, and confirming the `/records/:entityName` route works generically (not just for `crm.customers` by name coincidence — though there's only one entity to actually test against).
