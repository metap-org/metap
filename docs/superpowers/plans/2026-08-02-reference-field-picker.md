# Reference Field Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add real support for `EntityField.kind === "reference"`: a `refDisplayField` metadata convention (with boot-time validation), a searchable-autocomplete picker on write, a resolved-label renderer on read, and `referredBy` on `crm.customers` as the first real field using it.

**Architecture:** Backend gains one new optional `EntityField` property (`refDisplayField`) that flows to the wire unchanged (no serialization code to touch) plus a boot-time validation extension. Frontend gains two new small components (`ReferenceFieldInput`, `ReferenceFieldValue`) that `FieldInput`/`FieldValue` route `"reference"`-kind fields to instead of the generic string/text handling they fell through to before.

**Tech Stack:** TypeScript, Zod, Vitest (backend tests), React, Mantine `Select`, `@tanstack/react-query` (`useApiQuery`), `@mantine/hooks`' `useDebouncedValue` — no new dependency.

## Global Constraints

- No new dependency.
- No referential-integrity enforcement on write (a reference's id is never checked against `refEntity` before saving) — consistent with the existing no-FK JSONB `records` model.
- No self-reference cycle prevention.
- No N+1 batching for `ReferenceFieldValue` in lists — React Query dedupes identical `(entity, id)` query keys, that's the only optimization in scope.
- `refDisplayField` should itself be a `searchable: true` field on its target entity (for the picker's search to do substring `ILIKE` matching) — documented convention, not enforced by validation.
- `packages/platform-react` has no test framework — verification is `pnpm typecheck`/`pnpm build`/`pnpm lint` plus a best-effort manual browser check (this sandbox has had no working headless Chromium all session — missing `libnspr4.so` and other system libs, no `sudo`; report that honestly if still true rather than claiming success without it).

---

### Task 1: `refDisplayField` metadata — type, wire schema, boot-time validation

**Files:**
- Modify: `packages/core/src/core/metadata/entity.ts`
- Modify: `packages/core/src/core/metadata/entity-wire-schema.ts`
- Modify: `packages/core/src/core/metadata/metadata-registry.ts`
- Test: `packages/core/src/core/metadata/metadata-registry.test.ts`

**Interfaces:**
- Produces: `EntityField.refDisplayField?: string` — later tasks (2, 3, 5) read this property.

- [ ] **Step 1: Write the failing tests**

In `packages/core/src/core/metadata/metadata-registry.test.ts`, add two tests inside the existing `describe("MetadataRegistry.validateReferences", ...)` block (after the existing two tests, before the closing `});` at line 49), following the file's existing `widgetEntity()` fixture helper pattern exactly:

```ts
  it("does not throw when refDisplayField names a real field on the target entity", () => {
    const registry = new MetadataRegistry();
    registry.register(widgetEntity({ name: "test.owners" }));
    registry.register(
      widgetEntity({
        name: "test.widgets",
        fields: [
          { name: "name", label: "Name", kind: "string" },
          {
            name: "ownerId",
            label: "Owner",
            kind: "reference",
            refEntity: "test.owners",
            refDisplayField: "name",
          },
        ],
      }),
    );

    expect(() => registry.validateReferences()).not.toThrow();
  });

  it("throws when refDisplayField names a field that doesn't exist on the target entity", () => {
    const registry = new MetadataRegistry();
    registry.register(widgetEntity({ name: "test.owners" }));
    registry.register(
      widgetEntity({
        name: "test.widgets",
        fields: [
          { name: "name", label: "Name", kind: "string" },
          {
            name: "ownerId",
            label: "Owner",
            kind: "reference",
            refEntity: "test.owners",
            refDisplayField: "nickname",
          },
        ],
      }),
    );

    expect(() => registry.validateReferences()).toThrow(MetadataValidationError);
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm --filter @metap/core exec vitest run src/core/metadata/metadata-registry.test.ts`
Expected: FAIL — TypeScript error (`refDisplayField` doesn't exist on the field object literal's inferred type) or, if TS is lenient here, a runtime failure because `validateReferences()` doesn't check it yet, so the "throws when ... doesn't exist" test fails (nothing throws).

- [ ] **Step 3: Add `refDisplayField` to `EntityField`**

In `packages/core/src/core/metadata/entity.ts`, in the `EntityField` type, add one line right after `refEntity?: string;`:

```ts
export type EntityField = {
  name: string;
  label: string;
  kind: FieldKind;
  required?: boolean;
  indexed?: boolean;
  unique?: boolean;
  enumValues?: readonly string[];
  refEntity?: string;
  refDisplayField?: string;
  searchable?: boolean;
  searchMode?: "substring" | "fts"; // default: "substring" — only meaningful when searchable: true
  sortable?: boolean;
};
```

- [ ] **Step 4: Mirror it on the wire schema**

In `packages/core/src/core/metadata/entity-wire-schema.ts`, in `EntityFieldSchema`, add one line right after `refEntity: z.string().optional(),`:

```ts
export const EntityFieldSchema = z.object({
  name: z.string(),
  label: z.string(),
  kind: FieldKindSchema,
  required: z.boolean().optional(),
  indexed: z.boolean().optional(),
  unique: z.boolean().optional(),
  enumValues: z.array(z.string()).optional(),
  refEntity: z.string().optional(),
  refDisplayField: z.string().optional(),
  searchable: z.boolean().optional(),
  searchMode: z.enum(["substring", "fts"]).optional(),
  sortable: z.boolean().optional(),
});
```

No other wire-serialization code needs to change: `MetadataRegistry.toMetadata()` (in `metadata-registry.ts`) already passes `entity.fields` straight through into `EntitySummary`, so `refDisplayField` reaches `GET /metadata/entities(/:entity)` automatically, exactly like `refEntity` does today.

- [ ] **Step 5: Extend `validateReferences()`**

In `packages/core/src/core/metadata/metadata-registry.ts`, replace the `validateReferences()` method:

```ts
  validateReferences(): void {
    for (const entity of this.entities.values()) {
      const issues: string[] = [];
      for (const field of entity.fields) {
        if (field.kind === "reference" && field.refEntity && !this.entities.has(field.refEntity)) {
          issues.push(`field "${field.name}" references unknown entity "${field.refEntity}"`);
        }
        if (field.kind === "reference" && field.refEntity && field.refDisplayField) {
          const target = this.entities.get(field.refEntity);
          if (target && !target.fields.some((f) => f.name === field.refDisplayField)) {
            issues.push(
              `field "${field.name}" has refDisplayField "${field.refDisplayField}" which does not exist on "${field.refEntity}"`,
            );
          }
        }
      }
      if (issues.length > 0) {
        throw new MetadataValidationError(entity.name, issues);
      }
    }
  }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `pnpm --filter @metap/core exec vitest run src/core/metadata/metadata-registry.test.ts`
Expected: PASS (4 tests in this describe block, all green).

- [ ] **Step 7: Commit**

```bash
git add packages/core/src/core/metadata/entity.ts \
  packages/core/src/core/metadata/entity-wire-schema.ts \
  packages/core/src/core/metadata/metadata-registry.ts \
  packages/core/src/core/metadata/metadata-registry.test.ts
git commit -m "Add refDisplayField metadata convention with boot-time validation"
```

---

### Task 2: `referredBy` on `crm.customers`

**Files:**
- Modify: `apps/crm/src/modules/crm/customer.entity.ts`

**Interfaces:**
- Consumes: `refDisplayField` (Task 1).
- Produces: the first real `"reference"`-kind field in the codebase — Tasks 3 and 4's frontend components are exercised against this field.

- [ ] **Step 1: Add the schema property and field definition**

In `apps/crm/src/modules/crm/customer.entity.ts`, add `referredBy` to `CustomerSchema` (after `status`):

```ts
const CustomerSchema = z.object({
  code: z.string().min(1).max(80),
  name: z.string().min(1).max(255),
  phone: z.string().max(40).optional(),
  email: z.string().email().optional(),
  status: z.enum(["draft", "active", "blocked"]).default("draft"),
  referredBy: z.string().uuid().optional(),
});
```

Add a new entry to the `fields` array, after the `status` field entry:

```ts
    {
      name: "status",
      label: "Status",
      kind: "enum",
      enumValues: ["draft", "active", "blocked"],
      indexed: true,
      sortable: true,
    },
    {
      name: "referredBy",
      label: "Referred By",
      kind: "reference",
      refEntity: "crm.customers",
      refDisplayField: "name",
    },
```

Do not add `"referredBy"` to the `listViews` `"default"` view's `fields`/`filters` arrays — deliberately out of scope, keeps the list page's columns unchanged.

- [ ] **Step 2: Typecheck**

Run: `pnpm --filter @metap/crm exec tsc --noEmit`
Expected: PASS.

- [ ] **Step 3: Run the backend test suite as a regression check**

Run: `pnpm --filter @metap/core test`
Expected: all passing — this task doesn't touch `packages/core`'s own logic, `apps/crm` has no dedicated test suite of its own.

- [ ] **Step 4: Commit**

```bash
git add apps/crm/src/modules/crm/customer.entity.ts
git commit -m "Add referredBy self-reference field to crm.customers"
```

---

### Task 3: `ReferenceFieldInput` (write side) + wire into `FieldInput`

**Files:**
- Create: `packages/platform-react/src/field/ReferenceFieldInput.tsx`
- Modify: `packages/platform-react/src/field/FieldInput.tsx`

**Interfaces:**
- Consumes: `useApiQuery` (`../api/useApiQuery`, signature `useApiQuery<TFetched, TSelected = TFetched>(queryKey: QueryKey, path: string, select?: (data: TFetched) => TSelected, enabled: boolean = true)`), `EntityField` (`../metadata/types`).
- Produces: `ReferenceFieldInput` component with the same per-kind prop shape every other `FieldInput.tsx` branch's inline JSX uses: `{ field: EntityField; value: unknown; onChange: (value: unknown) => void; error?: string; disabled?: boolean }`.

- [ ] **Step 1: Create `ReferenceFieldInput.tsx`**

```tsx
import { useState } from "react";
import { Select } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { useApiQuery } from "../api/useApiQuery";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

function labelFor(record: RecordDto, refDisplayField: string | undefined): string {
  const raw = refDisplayField ? record.data[refDisplayField] : undefined;
  return typeof raw === "string" ? raw : record.id;
}

export function ReferenceFieldInput({
  field,
  value,
  onChange,
  error,
  disabled,
}: {
  field: EntityField;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
  disabled?: boolean;
}) {
  const label = field.label + (field.required ? " *" : "");
  const description = disabled ? "You can't edit this field" : undefined;
  const refEntity = field.refEntity;
  const currentValue = typeof value === "string" ? value : null;

  const [searchInput, setSearchInput] = useState("");
  const [debouncedSearch] = useDebouncedValue(searchInput, 300);

  const { data: currentRecord } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, currentValue],
    `/api/${refEntity}/${currentValue}`,
    (response) => response.data,
    Boolean(refEntity && currentValue),
  );

  const { data: searchResults } = useApiQuery<{ data: RecordDto[] }, RecordDto[]>(
    ["reference-search", refEntity, field.refDisplayField, debouncedSearch],
    `/api/${refEntity}?${field.refDisplayField}=${encodeURIComponent(debouncedSearch)}&limit=10`,
    (response) => response.data,
    Boolean(refEntity && field.refDisplayField && debouncedSearch.length > 0),
  );

  const options = new Map<string, string>();
  if (currentRecord) {
    options.set(currentRecord.id, labelFor(currentRecord, field.refDisplayField));
  }
  for (const record of searchResults ?? []) {
    options.set(record.id, labelFor(record, field.refDisplayField));
  }

  return (
    <Select
      label={label}
      description={description}
      searchable
      data={[...options.entries()].map(([optionValue, optionLabel]) => ({
        value: optionValue,
        label: optionLabel,
      }))}
      value={currentValue}
      searchValue={searchInput}
      onSearchChange={setSearchInput}
      onChange={(selected) => onChange(selected ?? undefined)}
      error={error}
      disabled={disabled}
    />
  );
}
```

`labelFor` mirrors `fieldKindConfig.ts`'s existing non-`"[object Object]"` coercion caution: it only trusts a `string` value from the target record's data and falls back to the record's own `id` otherwise, rather than calling `String()` on an untyped JSONB value.

- [ ] **Step 2: Wire it into `FieldInput.tsx`**

In `packages/platform-react/src/field/FieldInput.tsx`, add the import:

```tsx
import { ReferenceFieldInput } from "./ReferenceFieldInput";
```

Split the shared `case "string": case "reference":` branch (currently rendering one `TextInput` for both) into two:

```tsx
    case "string":
      return (
        <TextInput
          label={label}
          description={description}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
          disabled={disabled}
        />
      );
    case "reference":
      return (
        <ReferenceFieldInput
          field={field}
          value={value}
          onChange={onChange}
          error={error}
          disabled={disabled}
        />
      );
```

- [ ] **Step 3: Typecheck**

Run: `pnpm --filter @metap/platform-react exec tsc --noEmit`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add packages/platform-react/src/field/ReferenceFieldInput.tsx \
  packages/platform-react/src/field/FieldInput.tsx
git commit -m "Add ReferenceFieldInput: searchable-autocomplete picker for reference fields"
```

---

### Task 4: `ReferenceFieldValue` (read side) + wire into `FieldValue`

**Files:**
- Create: `packages/platform-react/src/field/ReferenceFieldValue.tsx`
- Modify: `packages/platform-react/src/field/FieldValue.tsx`
- Modify: `packages/platform-react/src/field/fieldKindConfig.ts`

**Interfaces:**
- Consumes: `useApiQuery` (`../api/useApiQuery`), `EntityField` (`../metadata/types`).
- Produces: `ReferenceFieldValue` component: `{ field: EntityField; value: unknown }`.

- [ ] **Step 1: Create `ReferenceFieldValue.tsx`**

```tsx
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

  const { data: record } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, id],
    `/api/${refEntity}/${id}`,
    (response) => response.data,
    Boolean(refEntity && id),
  );

  if (!id) {
    return <>—</>;
  }

  const raw = record && field.refDisplayField ? record.data[field.refDisplayField] : undefined;
  return <>{typeof raw === "string" ? raw : id}</>;
}
```

- [ ] **Step 2: Wire it into `FieldValue.tsx`**

In `packages/platform-react/src/field/FieldValue.tsx`, add the import:

```tsx
import { ReferenceFieldValue } from "./ReferenceFieldValue";
```

Add a `"reference"` branch alongside the existing `"enum"` branch, before the generic `formatFieldValue` fallback (the early null/undefined/masked-field return at the top of the function, lines 6-17, stays exactly as-is — it already runs before this point and handles the permission-masking case, unrelated to this change):

```tsx
  if (field.kind === "reference") {
    return <ReferenceFieldValue field={field} value={value} />;
  }

  const formatted = formatFieldValue(field.kind, value) ?? "—";

  if (field.kind === "enum") {
    return <Badge variant="light">{formatted}</Badge>;
  }

  return <>{formatted}</>;
```

(The new `if` block goes right after the existing null/undefined early-return block and before the `const formatted = ...` line — `"reference"` now never reaches `formatFieldValue`.)

- [ ] **Step 3: Remove the now-dead `"reference"` case from `formatFieldValue`**

In `packages/platform-react/src/field/fieldKindConfig.ts`, change:

```ts
    case "id":
    case "string":
    case "reference":
    case "enum":
      return safeString(value);
```

to:

```ts
    case "id":
    case "string":
    case "enum":
      return safeString(value);
```

- [ ] **Step 4: Typecheck**

Run: `pnpm --filter @metap/platform-react exec tsc --noEmit`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/platform-react/src/field/ReferenceFieldValue.tsx \
  packages/platform-react/src/field/FieldValue.tsx \
  packages/platform-react/src/field/fieldKindConfig.ts
git commit -m "Add ReferenceFieldValue: resolve a reference field's display label on read"
```

---

### Task 5: Regenerate frontend metadata types + full workspace verification

**Files:**
- Modify (generated, committed): `packages/platform-react/src/metadata/generated-types.ts`

**Interfaces:**
- Consumes: `refDisplayField` on the wire (Task 1), `referredBy` on `crm.customers` (Task 2).

- [ ] **Step 1: Start the backend**

Ensure `docker compose up -d postgres rabbitmq` is running, then run `pnpm dev` (repo root) in the background and wait for it to report ready.

- [ ] **Step 2: Regenerate types**

Run: `pnpm --filter @metap/platform-react generate:types`
Expected: `packages/platform-react/src/metadata/generated-types.ts` is rewritten; `git diff` on it shows `refDisplayField` added to the generated `EntityField`-equivalent type.

- [ ] **Step 3: Stop the backend dev server**

Stop the `pnpm dev` process started in Step 1.

- [ ] **Step 4: Full workspace typecheck, build, lint**

Run: `pnpm typecheck` (repo root, recursive)
Expected: no errors.

Run: `pnpm --filter @metap/demo build`
Expected: production build succeeds.

Run: `pnpm lint` (repo root, recursive)
Expected: same pre-existing baseline as before (the one `AuthContext.tsx` fast-refresh warning), no new errors.

- [ ] **Step 5: Full backend test regression**

Run: `pnpm test` (repo root)
Expected: all passing, including the 2 new tests from Task 1.

- [ ] **Step 6: Manual browser verification (best-effort)**

Start `docker compose up -d postgres rabbitmq` (if not already up), `pnpm dev` (API, repo root), `pnpm dev:web` (frontend). Log in via `/dev-login`, open a `crm.customers` record's edit form (or create one), and confirm:
- The "Referred By" field is a searchable Select, not a free-text box.
- Typing part of another existing customer's name shows it as a selectable option.
- Selecting one, saving, then viewing the record's detail page shows the referred customer's *name*, not a raw UUID.
- Editing that same record again shows the previously-selected customer's name pre-filled in the picker, not blank.

This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries — `libnspr4.so` and others, no `sudo`, no cached alternative) — if that's still true, report it plainly rather than claiming visual verification succeeded; typecheck/build/lint/tests are the actual verification available here.

- [ ] **Step 7: Commit**

```bash
git add packages/platform-react/src/metadata/generated-types.ts
git commit -m "Regenerate frontend metadata types for refDisplayField"
```
