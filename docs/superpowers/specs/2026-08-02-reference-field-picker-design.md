# Reference Field Picker Design

## Problem

`FieldKind` has declared `"reference"` since early in the project, and `EntityField.refEntity?: string` already names which entity a reference field points at — but no entity actually uses it yet, and the UI never grew real support for it. `FieldInput.tsx` renders `"reference"` in the same branch as `"string"` (a plain `TextInput` — free-text entry of a raw id), and `FieldValue.tsx`/`fieldKindConfig.ts` render it as a raw string on read. There is no metadata convention for which field of the target entity to show as a human-readable label, so even a hand-typed id would show as an opaque UUID, not a name.

This was surfaced and explicitly deferred during the navigation-decoupling work (`docs/superpowers/specs/2026-08-02-platform-react-navigation-decoupling-design.md`), pending a `displayField`-style metadata decision. Investigation for this spec confirmed the gap is still exactly as described, and additionally found no entity in the codebase (`apps/crm/src/modules/crm/customer.entity.ts` is the only real entity) declares a `"reference"` field today — so this project's own trigger-based-evolution stance (see `docs/architectures/04-strategy.md`) argues for creating one real, concrete use before building general picker UI. `referredBy` on `crm.customers` (a self-reference — the customer who referred this one) is a natural real CRM concept and serves as that trigger.

## Scope

1. A new `EntityField.refDisplayField?: string` metadata convention: names the field on the target entity (`refEntity`) whose value is shown as the human-readable label for a reference.
2. `MetadataRegistry.validateReferences()` extended to fail loudly at boot if `refDisplayField` names a field that doesn't exist on the target entity (mirrors its existing `refEntity`-must-exist check).
3. `referredBy` added to `crm.customers` as the first real reference field (`refEntity: "crm.customers"`, `refDisplayField: "name"`), self-referencing the same entity.
4. `packages/platform-react` gets real reference-field support: a searchable-autocomplete picker on write (`ReferenceFieldInput`), and a resolved-label renderer on read (`ReferenceFieldValue`).

Out of scope (recorded so it isn't a surprise later, not because it's forgotten):
- No referential-integrity enforcement on write — `CrudService` does not verify a reference's id actually exists in `refEntity` before saving. Consistent with the existing no-FK, generic-JSONB-`records`-table model (see `docs/architectures/05-building-blocks.md`'s Data Model Strategy); a bad id simply fails to resolve a label on read (falls back to showing the raw id) rather than being rejected on write.
- No self-reference cycle prevention (a customer could name itself as its own referrer). Cosmetic, not a data-integrity concern.
- No N+1 batching/optimization for resolving reference labels in list views. Each `ReferenceFieldValue` instance fetches independently via `useApiQuery`; React Query dedupes identical `(entity, id)` keys automatically, but a list page with many *distinct* referenced records still issues one request per distinct id. Acceptable at `crm.customers`' current scale (max 100 records/page); revisit if it becomes a measured problem, not preemptively.
- `refDisplayField` being a `searchable: true` field on its target entity is a convention this design relies on for the picker's search-as-you-type to do substring (`ILIKE`) matching rather than exact matching — not enforced by validation, just documented at the declaration site.

## Backend design (`packages/core`)

**`packages/core/src/core/metadata/entity.ts`** — add one field to `EntityField`:

```ts
export type EntityField = {
  // ...existing fields unchanged...
  refEntity?: string;
  refDisplayField?: string; // field on refEntity shown as this reference's human-readable label; convention: should be `searchable: true` on refEntity for autocomplete search to do substring matching
  // ...
};
```

**`packages/core/src/core/metadata/entity-wire-schema.ts`** — mirror it on `EntityFieldSchema`:

```ts
export const EntityFieldSchema = z.object({
  // ...
  refEntity: z.string().optional(),
  refDisplayField: z.string().optional(),
  // ...
});
```

This is the only wire-schema change needed — `MetadataRegistry.toMetadata()` already passes `entity.fields` straight through into `EntitySummary`, so `refDisplayField` reaches `GET /metadata/entities(/:entity)` with no separate mapping step, exactly like `refEntity` does today.

**`packages/core/src/core/metadata/metadata-registry.ts`** — extend `validateReferences()`:

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

The `target &&` guard skips the new check when `refEntity` itself is already unknown (that case is reported by the first check; no need to double-report or throw on `undefined.fields`).

**`apps/crm/src/modules/crm/customer.entity.ts`** — add the real field:

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

```ts
{
  name: "referredBy",
  label: "Referred By",
  kind: "reference",
  refEntity: "crm.customers",
  refDisplayField: "name",
},
```

(Appended to `fields`, after `status`; not added to the default `listView`'s `fields`/`filters` — this is a detail-page/form field, keeping the list column set unchanged is a deliberate minimal-scope choice, not an oversight.)

## Frontend design (`packages/platform-react`)

**Write side — `packages/platform-react/src/field/ReferenceFieldInput.tsx`** (new file):

A self-contained component with the same per-kind prop shape `FieldInput.tsx`'s other branches use (`field`, `value`, `onChange`, `error`, `disabled`). Internally:
- Mantine `Select` with `searchable`, `data` populated from `useApiQuery<{ data: RecordDto[] }, RecordDto[]>(["reference-search", field.refEntity, field.refDisplayField, debouncedQuery], `/api/${field.refEntity}?${field.refDisplayField}=${debouncedQuery}&limit=10`, (r) => r.data)`, debounced ~300ms via `useDebouncedValue` (same pattern `GeneratedList` already uses for text filters). Each option's `value` is the record's `id`; its `label` is `record.data[field.refDisplayField]`.
- A second query resolves the current value's label when the field already has one (editing an existing record): `useApiQuery(["record", field.refEntity, value], `/api/${field.refEntity}/${value}`, ..., enabled: typeof value === "string")`. Its result seeds the initially-shown label so opening an edit form doesn't show a blank picker or raw id before the user types anything.
- `onChange` receives the selected option's `value` (the target record's id) and calls the field's `onChange` with it, same contract every other `FieldInput` branch already follows.

**`FieldInput.tsx`** — split `"reference"` out of the `"string"` case:

```tsx
case "string":
  return ( /* unchanged TextInput branch */ );
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

(`field.refEntity` is guaranteed present for any `"reference"`-kind field that passed `validateReferences()` at boot, so `ReferenceFieldInput` can assume it's set rather than re-validating in the UI.)

**Read side — `packages/platform-react/src/field/ReferenceFieldValue.tsx`** (new file):

```tsx
export function ReferenceFieldValue({ field, value }: { field: EntityField; value: unknown }) {
  const refEntity = field.refEntity;
  const id = typeof value === "string" ? value : undefined;
  const { data: record } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, id],
    `/api/${refEntity}/${id}`,
    (r) => r.data,
    Boolean(refEntity && id),
  );
  const label =
    record && field.refDisplayField
      ? (record.data[field.refDisplayField] ?? String(id))
      : String(id ?? "—");
  return <>{label}</>;
}
```

**`FieldValue.tsx`** — route `"reference"` to it instead of `formatFieldValue`:

```tsx
if (field.kind === "reference") {
  return <ReferenceFieldValue field={field} value={value} />;
}
```

placed alongside the existing `if (field.kind === "enum")` branch, before the generic `formatFieldValue` fallback. `fieldKindConfig.ts`'s `"reference"` case in `formatFieldValue` is removed (dead once `FieldValue` no longer calls it for this kind) — falls through to `"string"`'s `safeString(value)` behavior would otherwise silently keep working but never actually run, which is worse than deleting it.

## Metadata type generation

`refDisplayField` is a new property on the wire schema, so after the backend change, `pnpm dev` (to have a running server) then `pnpm --filter @metap/platform-react generate:types` must be re-run and the regenerated `generated-types.ts` committed — the established convention (`docs/superpowers/specs/2026-08-02-fe-metadata-generation-design.md`-equivalent process already in place, see `CLAUDE.md`'s "Metadata types stay generated, not hand-written").

## Testing

**Backend (TDD, `packages/core/src/core/metadata/metadata-registry.test.ts`):**
- Extend the existing `describe("MetadataRegistry.validateReferences")` block: one new test registering an entity whose reference field has a valid `refDisplayField` (must not throw), one new test with a `refDisplayField` naming a nonexistent field on the target entity (must throw `MetadataValidationError` with a message identifying the field).

**Frontend:** no test framework in `packages/platform-react` (established, unchanged boundary). Verification is `pnpm typecheck` (recursive), `pnpm --filter @metap/demo build`, `pnpm lint` (recursive), and a best-effort manual browser check on `crm.customers`' create/edit form and detail page — report the known sandbox limitation (no working headless Chromium) honestly if it still applies rather than claiming success without it.

## File summary

- Modify: `packages/core/src/core/metadata/entity.ts` (add `refDisplayField`)
- Modify: `packages/core/src/core/metadata/entity-wire-schema.ts` (mirror on `EntityFieldSchema`)
- Modify: `packages/core/src/core/metadata/metadata-registry.ts` (extend `validateReferences()`)
- Modify: `packages/core/src/core/metadata/metadata-registry.test.ts` (2 new tests)
- Modify: `apps/crm/src/modules/crm/customer.entity.ts` (add `referredBy` field + schema property)
- Create: `packages/platform-react/src/field/ReferenceFieldInput.tsx`
- Create: `packages/platform-react/src/field/ReferenceFieldValue.tsx`
- Modify: `packages/platform-react/src/field/FieldInput.tsx` (split `"reference"` out)
- Modify: `packages/platform-react/src/field/FieldValue.tsx` (route `"reference"` to `ReferenceFieldValue`)
- Modify: `packages/platform-react/src/field/fieldKindConfig.ts` (remove dead `"reference"` case)
- Modify (generated, committed): `packages/platform-react/src/metadata/generated-types.ts`
