# FieldRenderer (FieldValue): Metadata-Driven Read-Only Field Display

Date: 2026-08-02

Status: approved

Scope: first of five planned Phase 6 (Frontend Core) sub-projects, in this priority order:

1. **FieldRenderer foundation (`FieldValue`)** (this spec)
2. `GeneratedForm` (uses this spec's `fieldKindConfig` to build `FieldInput`)
3. `WorkflowActionBar`
4. Permission-aware UI state
5. Pagination / table virtualization for `GeneratedList`

Each sub-project ships independently, matching how Phase 3/4 were run.

## Motivation

`GeneratedList` (`web/src/platform/list/GeneratedList.tsx`) currently renders every field value the same way, regardless of its declared `kind`:

```tsx
{listView.fields.map((fieldName) => (
  <Table.Td key={fieldName}>{String(record.data[fieldName] ?? "")}</Table.Td>
))}
```

This is also the source of this repo's one pre-existing frontend lint error (`@typescript-eslint/no-base-to-string` on this exact line — `record.data[fieldName]` is `unknown`, and `String()` on a non-primitive falls back to `"[object Object]"`). A boolean renders as `"true"`/`"false"`, a date as a raw ISO string, an enum as its raw value with no visual distinction — none of it reflects the field's declared `kind`.

`docs/roadmap.md`'s Phase 6 goals list `FieldRenderer` as a deliverable, alongside `GeneratedForm` (which will need the same kind-to-widget mapping for editable inputs, not just display). This spec builds the read-only half only — `FieldValue` — plus the shared kind-to-formatter table it and the future `FieldInput` (sub-project 2) will both draw from. Building `FieldInput` now would sit unused until `GeneratedForm` exists to call it.

## Design

### `fieldKindConfig.ts`: the shared foundation

`web/src/platform/field/fieldKindConfig.ts` exports a lookup from `FieldKind` to a display formatter:

```ts
export type FieldKind =
  | "id" | "string" | "number" | "boolean" | "date" | "datetime"
  | "money" | "enum" | "reference" | "json";

const numberFormatter = new Intl.NumberFormat();
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });
const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function formatFieldValue(kind: FieldKind, value: unknown): string | null {
  if (value === null || value === undefined) {
    return null;
  }

  switch (kind) {
    case "number":
    case "money":
      return typeof value === "number" ? numberFormatter.format(value) : String(value);
    case "boolean":
      return value ? "Yes" : "No";
    case "date":
      return typeof value === "string" ? dateFormatter.format(new Date(value)) : String(value);
    case "datetime":
      return typeof value === "string" ? dateTimeFormatter.format(new Date(value)) : String(value);
    case "json":
      return JSON.stringify(value);
    case "id":
    case "string":
    case "reference":
    case "enum":
      // These kinds are always strings on the wire, per EntityField's
      // Zod-validated backend shape — no [object Object] risk here, unlike
      // the untyped String(unknown) this replaces.
      return String(value);
  }
}
```

`enum` gets special treatment in `FieldValue` itself (rendered as a Mantine `Badge`, not plain text) rather than in `formatFieldValue`, since that's a display-component concern, not a value-formatting one — `formatFieldValue("enum", value)` still returns the plain string for any caller that just wants text (e.g. a future CSV export).

### `FieldValue.tsx`: the read-only display component

```tsx
export function FieldValue({ field, value }: { field: EntityField; value: unknown }) {
  if (value === null || value === undefined) {
    return <>—</>;
  }
  if (field.kind === "enum") {
    return <Badge variant="light">{formatFieldValue(field.kind, value)}</Badge>;
  }
  return <>{formatFieldValue(field.kind, value)}</>;
}
```

`reference` fields display the raw referenced id as plain text — resolving it to the referenced entity's label (e.g. showing a customer's name instead of its uuid) needs a second fetch per reference field and is explicitly deferred (see Out of scope).

### Metadata type update

`web/src/platform/metadata/types.ts`'s `EntityField.kind` is currently `kind: string` — loosened from the backend's real `FieldKind` union (`src/core/metadata/entity.ts`) because nothing on the frontend switched on it yet. `FieldValue` needs to switch on it exhaustively, so `kind` becomes the same union (mirrored, not imported — the frontend has no build-time dependency on backend source, per `docs/architectures/05-building-blocks.md`'s Development View: `web/` only ever reaches the backend over HTTP). `refEntity?: string` is also added to `EntityField` now (unused until reference-resolution is built, but it's already present on the backend's `/metadata/entities` response today, so typing it now costs nothing and avoids a second metadata-type edit later).

### Wiring into `GeneratedList`

The one line quoted in Motivation becomes:

```tsx
<Table.Td key={fieldName}>
  <FieldValue field={field} value={record.data[fieldName]} />
</Table.Td>
```

using the same `field` (an `EntityField`) already looked up via `fieldsByName.get(fieldName)` a few lines above it in the existing header-rendering code — no new lookup needed, just reusing what's already in scope. This also removes the pre-existing `no-base-to-string` lint error as a side effect, not a separate cleanup pass.

## Out of scope (deliberate, not an oversight)

- **`FieldInput` (editable widgets).** Sub-project 2 (`GeneratedForm`) builds this against `fieldKindConfig`'s shared formatter table.
- **Resolving `reference` fields to the referenced record's label.** Needs a fetch per reference field (or a batched resolver) — no current UI need for it since `crm.customers` (the only entity) has no `reference`-kind fields today.
- **Currency-aware `money` formatting** (currency code/symbol). No currency field exists in metadata yet; formatted as a plain number for now.
- **Locale selection.** `Intl.NumberFormat`/`Intl.DateTimeFormat` use the browser's default locale — no explicit locale prop or user preference, since nothing in this app has a locale concept yet.

## Testing

`web/` has no test framework configured yet (no vitest/testing-library dependency, no existing `*.test.tsx` files) — this is the first frontend work this session, and adding a whole test harness is out of scope for a small display component. Verification is: `tsc -b` (via `pnpm build` in `web/`) for type correctness, `oxlint` for the existing lint setup, and manually exercising `GeneratedList` in a browser against the running dev server (`pnpm dev` in `web/`) to confirm boolean/date/enum/null values render as intended — per this project's own convention that UI changes need a real browser check, not just a green typecheck.
