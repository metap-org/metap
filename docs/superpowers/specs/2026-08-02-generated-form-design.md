# GeneratedForm: Create/Edit Record UI + Field-Level Backend Errors

Date: 2026-08-02

Status: approved

Scope: second of five planned Phase 6 (Frontend Core) sub-projects, in this priority order:

1. FieldRenderer foundation (`FieldValue`) — done, `docs/superpowers/specs/2026-08-02-field-renderer-design.md`
2. **`GeneratedForm`** (this spec)
3. `WorkflowActionBar`
4. Permission-aware UI state
5. Pagination / table virtualization for `GeneratedList`

This sub-project's scope grew during brainstorming to include a small backend change (field-level error detail) — the user explicitly chose that over shipping a rougher error UX now and tracking it as a gap, so it's in scope here, not deferred.

## Motivation

`CrudService.create()`/`update()` (`src/core/crud/crud-service.ts`) currently return bare error codes on failure — `{ ok: false, status: 400, error: "validation_failed" }` with no indication of *which* field(s) failed Zod validation, and `{ ok: false, status: 403, error: "forbidden" }` from `assertWritableFields` with no indication of *which* field's write policy denied the request (the check returns on the first denied field, discarding which one). A form can't show inline, per-field errors against either failure mode today — it can only show a generic banner. Since `GeneratedForm` is meant to be the reusable create/edit UI for every metadata-driven entity (not just `crm.customers`), shipping it without real field-level errors would be a rough, first-class UX gap in the one piece of UI whose entire job is collecting field input.

## Design

### Backend: field-level error detail

**`ServiceResult`** (`src/core/crud/result.ts`) failure variant gains an optional field:

```ts
export type ServiceResult<T> =
  | { ok: true; data: T; page?: unknown }
  | {
      ok: false;
      status: number;
      error: string;
      message?: string;
      fieldErrors?: Record<string, string[]>;
    };
```

**`PermissionDecision`** (`src/core/permission/permission-service.ts`) gains an optional `field`:

```ts
export type PermissionDecision = {
  allowed: boolean;
  reason?: string;
  field?: string;
};
```

**`PermissionSnapshot.assertWritableFields`** (`src/core/permission/permission-snapshot.ts`) returns the denied field name:

```ts
      if (!passed) {
        return { allowed: false, reason: "forbidden", field };
      }
```

**`CrudService.create()`/`update()`** populate `fieldErrors` at both existing failure points:

```ts
// validation_failed:
const parsed = entity.schema.safeParse(rawData);
if (!parsed.success) {
  return {
    ok: false,
    status: 400,
    error: "validation_failed",
    fieldErrors: parsed.error.flatten().fieldErrors,
  };
}

// forbidden (write policy):
const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), existingData);
if (!writeDecision.allowed) {
  return {
    ok: false,
    status: 403,
    error: writeDecision.reason ?? "forbidden",
    fieldErrors: writeDecision.field ? { [writeDecision.field]: ["forbidden"] } : undefined,
  };
}
```

`parsed.error.flatten().fieldErrors` is Zod v4's built-in shape — verified directly against the installed `zod` package during brainstorming: `{ fieldName: ["human-readable message", ...] }`.

**`sendServiceError`** (`src/server/error-handler.ts`) forwards `fieldErrors` into the JSON response, nested inside the existing `error` object (not a new top-level key, so the response shape grows additively):

```ts
function errorBody(
  request: FastifyRequest,
  code: string,
  message: string,
  fieldErrors?: Record<string, string[]>,
): ErrorBody {
  return {
    error: {
      code,
      message,
      requestId: request.id,
      traceId: request.traceId,
      ...(fieldErrors ? { fieldErrors } : {}),
    },
  };
}
```

`sendServiceError` passes `result.fieldErrors` through. The generic route-level `ZodError` handler in the same file (triggered by malformed *request shape*, e.g. a missing `data` key — a different failure than entity-data validation) is untouched; this is specifically about `CrudService`'s business-data validation path.

### Frontend: `useApiMutation`

`web/src/platform/api/useApiMutation.ts` — the `useMutation` counterpart to the existing `useApiQuery` (`useQuery` wrapper), same shape: pulls the token from `AuthContext`, calls `apiFetch` with a method/body, and on a thrown `ApiError` exposes `error.fieldErrors` (the existing `ApiError` class in `web/src/platform/api/client.ts` gains an optional `fieldErrors?: Record<string, string[]>` field, populated from the response body's new `error.fieldErrors`, mirroring how `ApiError` already carries `status`/`code`/`message` from that same body).

### Frontend: `FieldInput`

`web/src/platform/field/FieldInput.tsx` — the editable counterpart to sub-project 1's `FieldValue`, built against the same `fieldKindConfig.ts` foundation. One widget per `FieldKind`:

| Kind | Widget |
|---|---|
| `string`, `reference` | Mantine `TextInput` (reference: raw id text entry, no picker — matches `FieldValue`'s deferral) |
| `number`, `money` | Mantine `NumberInput` |
| `boolean` | Mantine `Checkbox` |
| `date` | `@mantine/dates`' `DateInput` (**new dependency** — added to `web/package.json`) |
| `datetime` | `@mantine/dates`' `DateTimePicker` |
| `enum` | Mantine `Select`, options from `field.enumValues` |
| `json` | Mantine `Textarea`, raw JSON text (parsed on submit; invalid JSON is a client-side error before the request is even sent) |
| `id` | Never rendered — `id` is server-generated, not part of any entity's writable field set |

Props: `{ field: EntityField; value: unknown; onChange: (value: unknown) => void; error?: string }`. `error` renders via each Mantine input's own `error` prop (all of the above support one) — this is what `GeneratedForm` feeds from `fieldErrors[field.name]`.

### Frontend: `GeneratedForm`

`web/src/platform/form/GeneratedForm.tsx` — one component, mode driven by an optional `recordId`:

```tsx
function GeneratedForm({ entityName, recordId, onSaved }: {
  entityName: string;
  recordId?: string;
  onSaved: (record: RecordDto) => void;
}) { /* ... */ }
```

- **Create** (`recordId` absent): local form state starts `{}`, submit is `POST /api/:entityName`.
- **Edit** (`recordId` present): `useApiQuery` fetches `GET /api/:entityName/:recordId` first (reusing the endpoint built earlier this session) to seed form state and capture `version`; submit is `PATCH /api/:entityName/:recordId` with that `version`.
- Renders one `FieldInput` per `entity.fields` entry that isn't `kind: "id"`, using `entity.fields`/`listView` from the same `useEntity` hook `GeneratedList` already uses.
- On submit failure: if the thrown `ApiError` has `fieldErrors`, each is routed to its matching `FieldInput`'s `error` prop; any top-level `message` (or a field error for a field not currently rendered — shouldn't happen given the entity's own fields, but handled defensively) shows in a banner above the form.
- On success: calls `onSaved(record)` — navigation/redirect behavior is the *caller's* concern (`App.tsx`/routes), not something `GeneratedForm` decides itself, keeping it a reusable, embeddable component rather than one that assumes it owns a page.

## Out of scope (deliberate, not an oversight)

- **Reference-field pickers.** `reference` kind stays raw-id text entry in `FieldInput`, matching `FieldValue`'s existing deferral (no entity has a `reference`-kind field yet).
- **Client-side Zod validation mirroring the backend schema.** Metadata's `EntityField.required` can hint required-ness in the UI (e.g. an asterisk), but real validation stays server-side only — no duplicate validation logic to keep in sync.
- **Field-level *read* masking awareness in the form** (e.g. hiding a field the caller can't write to). That's sub-project 4, Permission-aware UI state — this sub-project's error handling is reactive (show the 403 after a denied submit), not proactive (hide the field before the user tries).
- **Routing/page wiring** (adding a "New"/"Edit" route to `App.tsx`). `GeneratedForm` is built and usable as a component in this sub-project; wiring it into actual app routes/navigation is small and can happen here too during implementation, but isn't the focus of this design — it doesn't change `GeneratedForm`'s own contract.

## Testing

Backend changes (`ServiceResult`, `PermissionDecision`, `assertWritableFields`, `CrudService`, `error-handler.ts`) follow this project's existing TDD convention — minimal, targeted tests:
- One test: `create()` with invalid data returns `fieldErrors` matching the invalid field(s).
- One test: `update()` blocked by a field-level write policy returns `fieldErrors` naming that field.
- One test: the HTTP route surfaces `error.fieldErrors` in the response body (live-DB, via `app.inject`).

Frontend: `web/` has no test framework configured (same as sub-project 1) — verification is `tsc -b`/`pnpm build` + `oxlint` + a manual browser check (create a record, trigger a validation error, confirm it appears next to the right field; edit a record, confirm it loads and saves).
