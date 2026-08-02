# FieldRenderer (FieldValue) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render field values in `GeneratedList` according to their declared `kind` (formatted numbers/dates, Yes/No booleans, enum badges) instead of blind `String(value)`, and give `GeneratedForm` (next sub-project) a shared kind-to-formatter table to build editable inputs against.

**Architecture:** A pure `fieldKindConfig.ts` formatter function, a small `FieldValue` display component built on it, and one wiring change in `GeneratedList`. No backend changes.

**Tech Stack:** React 19, TypeScript, Mantine (`Badge`), Vite. `web/` has no test framework configured — verification is `tsc -b` + `oxlint` + manual browser check against the dev server, per this project's convention that UI changes need a real browser check.

## Global Constraints

- `web/` has zero build-time dependency on backend `src/` — types are mirrored, never imported across the boundary (`docs/architectures/05-building-blocks.md`'s Development View).
- No new test framework is introduced for this small change — matches the fact `web/` has no existing `*.test.tsx` anywhere yet.
- `FieldInput` (editable widgets) is explicitly out of scope — only `FieldValue` (read-only) ships in this sub-project.

---

### Task 1: `fieldKindConfig.ts` + `FieldValue.tsx`

**Files:**
- Create: `web/src/platform/field/fieldKindConfig.ts`
- Create: `web/src/platform/field/FieldValue.tsx`
- Modify: `web/src/platform/metadata/types.ts`

**Interfaces:**
- Produces:
  ```ts
  // fieldKindConfig.ts
  export type FieldKind =
    | "id" | "string" | "number" | "boolean" | "date" | "datetime"
    | "money" | "enum" | "reference" | "json";
  export function formatFieldValue(kind: FieldKind, value: unknown): string | null;

  // FieldValue.tsx
  export function FieldValue(props: { field: EntityField; value: unknown }): JSX.Element;
  ```
  Task 2 consumes `FieldValue` and `EntityField`'s updated `kind`/`refEntity` typing.

- [ ] **Step 1: Update `EntityField`'s type**

In `web/src/platform/metadata/types.ts`, replace:

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
```

with:

```ts
export type FieldKind =
  | "id"
  | "string"
  | "number"
  | "boolean"
  | "date"
  | "datetime"
  | "money"
  | "enum"
  | "reference"
  | "json";

export type EntityField = {
  name: string;
  label: string;
  kind: FieldKind;
  required?: boolean;
  searchable?: boolean;
  sortable?: boolean;
  enumValues?: readonly string[];
  refEntity?: string;
};
```

(`FieldKind` lives in `types.ts`, not `fieldKindConfig.ts`, since `types.ts` is already this codebase's home for metadata-shape types — `fieldKindConfig.ts` imports it from there in Step 2, it doesn't redeclare it.)

- [ ] **Step 2: Write `fieldKindConfig.ts`**

Create `web/src/platform/field/fieldKindConfig.ts`:

```ts
import type { FieldKind } from "../metadata/types";

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
      return String(value);
  }
}
```

- [ ] **Step 3: Write `FieldValue.tsx`**

Create `web/src/platform/field/FieldValue.tsx`:

```tsx
import { Badge } from "@mantine/core";
import type { EntityField } from "../metadata/types";
import { formatFieldValue } from "./fieldKindConfig";

export function FieldValue({ field, value }: { field: EntityField; value: unknown }) {
  if (value === null || value === undefined) {
    return <>—</>;
  }

  const formatted = formatFieldValue(field.kind, value) ?? "—";

  if (field.kind === "enum") {
    return <Badge variant="light">{formatted}</Badge>;
  }

  return <>{formatted}</>;
}
```

The `?? "—"` after `formatFieldValue` is defensive — every branch in `formatFieldValue`'s switch returns a non-null string once its `value === null || value === undefined` guard has already been passed, but TypeScript's return type (`string | null`) doesn't encode that across the two components, so this keeps the type honest without a non-null assertion.

- [ ] **Step 4: Typecheck and lint**

Run: `cd web && pnpm build` (runs `tsc -b && vite build` — this is the only typecheck path for `web/`, there's no separate `pnpm typecheck` script here) `&& pnpm lint`
Expected: no new errors. (`web/`'s `tsc -b` will also catch any other file that assumed `EntityField.kind` was a bare `string` — check the diagnostic output for any such file; none are known to exist beyond `GeneratedList.tsx`, handled in Task 2.)

- [ ] **Step 5: Commit**

```bash
git add web/src/platform/field/fieldKindConfig.ts web/src/platform/field/FieldValue.tsx web/src/platform/metadata/types.ts
git commit -m "Add FieldValue + fieldKindConfig: metadata-driven read-only field display"
```

---

### Task 2: Wire `FieldValue` into `GeneratedList`

**Files:**
- Modify: `web/src/platform/list/GeneratedList.tsx`

**Interfaces:**
- Consumes: `FieldValue` (Task 1).

- [ ] **Step 1: Replace the raw `String(...)` cell rendering**

In `web/src/platform/list/GeneratedList.tsx`, add the import:

```tsx
import { FieldValue } from "../field/FieldValue";
```

Replace:

```tsx
records?.map((record) => (
  <Table.Tr key={record.id}>
    {listView.fields.map((fieldName) => (
      <Table.Td key={fieldName}>{String(record.data[fieldName] ?? "")}</Table.Td>
    ))}
  </Table.Tr>
))
```

with:

```tsx
records?.map((record) => (
  <Table.Tr key={record.id}>
    {listView.fields.map((fieldName) => {
      const field = fieldsByName.get(fieldName);

      return (
        <Table.Td key={fieldName}>
          {field ? <FieldValue field={field} value={record.data[fieldName]} /> : null}
        </Table.Td>
      );
    })}
  </Table.Tr>
))
```

`fieldsByName` is the `Map<string, EntityField>` already built via `useMemo` near the top of this component — reused here, not recreated.

- [ ] **Step 2: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no errors — this also removes the pre-existing `@typescript-eslint/no-base-to-string` error on this exact line (part of the repo's overall lint baseline of 17 errors; that baseline count is tracked from the root `pnpm lint`, which includes `web/`, so confirm with a root-level `pnpm lint` too — expect 16 after this fix).

- [ ] **Step 3: Manual browser verification**

Run `docker compose up -d postgres rabbitmq` (if not already up), `pnpm dev` (repo root, API on :3000), and in a second terminal `cd web && pnpm dev` (:5173). Log in via `/dev-login` (mint a token with `pnpm mint-token` at the repo root first, per `README.md`), navigate to a `crm.customers` list, and confirm:
- `status` (an enum field) renders as a Mantine badge, not raw text.
- Any `null`/missing field value renders as `—`, not an empty cell or `"null"`.
- Existing filter/sort behavior in `GeneratedList` is unaffected (this change only touches cell rendering, not query logic).

- [ ] **Step 4: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing — this task touches no backend code, but confirms nothing else was accidentally disturbed.

- [ ] **Step 5: Commit**

```bash
git add web/src/platform/list/GeneratedList.tsx
git commit -m "GeneratedList: render field values via FieldValue instead of raw String()"
```
