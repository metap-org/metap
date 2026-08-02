# GeneratedForm Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A reusable `GeneratedForm` component for creating/editing any metadata-driven entity, with real inline field-level errors — which requires the backend to first start surfacing which field(s) a validation or write-policy failure actually names.

**Architecture:** Backend: `ServiceResult`/`PermissionDecision` grow an optional field-error channel, populated by `CrudService.create()`/`update()` from Zod's `flatten().fieldErrors` and from `assertWritableFields`'s now-named denied field, forwarded to the HTTP response by `sendServiceError`. Frontend: a new `useApiMutation` hook (mirrors `useApiQuery`), a new `FieldInput` component (mirrors `FieldValue`, one widget per `FieldKind`), and `GeneratedForm` composing both plus the existing `useEntity`/`GET /api/:entity/:id`.

**Tech Stack:** TypeScript, Zod, Fastify, Drizzle, Vitest (backend, live-DB tests); React 19, Mantine (+new `@mantine/dates` dependency), TanStack Query (frontend, no test framework — verification is typecheck/lint/manual browser check).

## Global Constraints

- `fieldErrors` is additive to the existing `ServiceResult`/HTTP error shape — every existing caller/consumer of `ServiceResult`/`ApiError` keeps working unchanged; only new optional fields are added.
- `reference`-kind fields stay raw-id text entry (no picker); `id`-kind fields are never rendered as inputs.
- No new frontend test framework — verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check, consistent with sub-project 1.
- Minimal, targeted backend tests only — extend existing tests where a near-identical one already exists, rather than duplicating.

---

### Task 1: Backend field-level errors

**Files:**
- Modify: `src/core/crud/result.ts`
- Modify: `src/core/permission/permission-service.ts`
- Modify: `src/core/permission/permission-snapshot.ts`
- Modify: `src/core/crud/crud-service.ts`
- Modify: `src/server/error-handler.ts`
- Test: `src/core/crud/crud-service.test.ts`, `src/server/app.test.ts`

**Interfaces:**
- Produces: `ServiceResult`'s `{ ok: false }` variant gains `fieldErrors?: Record<string, string[]>`; `PermissionDecision` gains `field?: string`; the HTTP error response body's `error` object gains an optional `fieldErrors` key. Consumed by Task 2/3's frontend `ApiError`/`useApiMutation`.

- [ ] **Step 1: Write the failing tests**

In `src/core/crud/crud-service.test.ts`, extend the existing `"rejects a create payload touching a field the caller cannot write"` test (inside `describe("CrudService field/record enforcement (live DB)", ...)`) — add after the existing `expect(result.status).toBe(403);`:

```ts
        expect(result.fieldErrors).toEqual({ phone: ["forbidden"] });
```

Add one new test in the same `describe` block, after the extended one:

```ts
  it("returns fieldErrors naming the invalid field(s) on a validation failure", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.create("crm.customers", { code: "" }, adminContext);

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.status).toBe(400);
      expect(result.fieldErrors?.code).toBeDefined();
      expect(result.fieldErrors?.name).toBeDefined();
    }
  });
```

(`{ code: "" }` fails `code: z.string().min(1)`; omitting `name` entirely fails `name: z.string().min(1)` too — both fields should appear in `fieldErrors`, per `src/modules/crm/customer.entity.ts`'s schema.)

In `src/server/app.test.ts`, inside the existing `describe("buildApp (record creation, live DB)", ...)` block, add one test after the "persists the posted data payload intact" test:

```ts
  it("surfaces fieldErrors in the response body on a validation failure", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const tenantId = "00000000-0000-0000-0000-000000000010";
    const userId = "00000000-0000-0000-0000-000000000011";
    const token = jwt.sign({ tenantId, roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: userId,
      expiresIn: "1h",
    });

    const response = await app.inject({
      method: "POST",
      url: "/api/crm.customers",
      headers: { authorization: `Bearer ${token}` },
      payload: { data: { code: "" } },
    });

    expect(response.statusCode).toBe(400);
    const body = response.json<{ error: { code: string; fieldErrors?: Record<string, string[]> } }>();
    expect(body.error.fieldErrors?.code).toBeDefined();
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts src/server/app.test.ts -t "fieldErrors"`
Expected: FAIL — `fieldErrors` doesn't exist on `ServiceResult`/the response body yet (assertions against `undefined`).

- [ ] **Step 3: Implement**

In `src/core/crud/result.ts`:

```ts
export type ServiceResult<T> =
  | {
      ok: true;
      data: T;
      page?: unknown;
    }
  | {
      ok: false;
      status: number;
      error: string;
      message?: string;
      fieldErrors?: Record<string, string[]>;
    };
```

In `src/core/permission/permission-service.ts`:

```ts
export type PermissionDecision = {
  allowed: boolean;
  reason?: string;
  field?: string;
};
```

In `src/core/permission/permission-snapshot.ts`, inside `assertWritableFields`, change:

```ts
      if (!passed) {
        return { allowed: false, reason: "forbidden" };
      }
```

to:

```ts
      if (!passed) {
        return { allowed: false, reason: "forbidden", field };
      }
```

In `src/core/crud/crud-service.ts`:

In `create()`, change:

```ts
    const parsed = entity.schema.safeParse(rawData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }
```

to:

```ts
    const parsed = entity.schema.safeParse(rawData);

    if (!parsed.success) {
      return {
        ok: false,
        status: 400,
        error: "validation_failed",
        fieldErrors: parsed.error.flatten().fieldErrors,
      };
    }
```

Apply the exact same change to `update()`'s identical `if (!parsed.success)` block.

In `create()`, change:

```ts
    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), undefined);

    if (!writeDecision.allowed) {
      return { ok: false, status: 403, error: writeDecision.reason ?? "forbidden" };
    }
```

to:

```ts
    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), undefined);

    if (!writeDecision.allowed) {
      return {
        ok: false,
        status: 403,
        error: writeDecision.reason ?? "forbidden",
        ...(writeDecision.field ? { fieldErrors: { [writeDecision.field]: ["forbidden"] } } : {}),
      };
    }
```

Note: `tsconfig.json` has `exactOptionalPropertyTypes: true`, which rejects an explicit `fieldErrors: undefined` on a property typed `fieldErrors?: ...` (that's a type error, not just a lint nit) — the conditional spread above omits the key entirely when there's no field, rather than setting it to `undefined`.

Apply the exact same change to `update()`'s identical `writeDecision` block.

In `src/server/error-handler.ts`, change `errorBody`:

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

Update the `ErrorBody` type just above it to include the optional field:

```ts
type ErrorBody = {
  error: {
    code: string;
    message: string;
    requestId: string;
    traceId: string;
    fieldErrors?: Record<string, string[]>;
  };
};
```

Update `sendServiceError` to pass it through:

```ts
export function sendServiceError(
  request: FastifyRequest,
  reply: FastifyReply,
  result: Extract<ServiceResult<unknown>, { ok: false }>,
) {
  const message = result.message ?? SERVICE_ERROR_MESSAGES[result.error] ?? result.error;
  return reply.code(result.status).send(errorBody(request, result.error, message, result.fieldErrors));
}
```

(The other three `errorBody(...)` call sites in this file — `AuthError`, `ZodError`, the generic `httpError` branch — are unchanged; they don't pass a 4th argument, which is fine since it's optional.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts src/server/app.test.ts`
Expected: PASS — all tests in both files.

- [ ] **Step 5: Typecheck, lint, full suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new lint errors (baseline 16, per this session's latest count); full suite passes (128 before this task + 2 = 130).

- [ ] **Step 6: Commit**

```bash
git add src/core/crud/result.ts src/core/permission/permission-service.ts src/core/permission/permission-snapshot.ts src/core/crud/crud-service.ts src/server/error-handler.ts src/core/crud/crud-service.test.ts src/server/app.test.ts
git commit -m "Surface field-level validation/permission errors through ServiceResult and the HTTP error body"
```

---

### Task 2: `useApiMutation` + `FieldInput`

**Files:**
- Modify: `web/src/platform/api/client.ts`
- Create: `web/src/platform/api/useApiMutation.ts`
- Create: `web/src/platform/field/FieldInput.tsx`
- Modify: `web/package.json` (add `@mantine/dates`)

**Interfaces:**
- Produces:
  ```ts
  // client.ts — ApiError gains:
  export class ApiError extends Error {
    readonly status: number;
    readonly code: string;
    readonly fieldErrors?: Record<string, string[]>;
    // constructor gains a 4th optional param, fieldErrors
  }

  // useApiMutation.ts
  export function useApiMutation<TResponse, TBody = unknown>(
    method: "POST" | "PATCH",
    path: string,
  ): UseMutationResult<TResponse, ApiError, TBody>;

  // FieldInput.tsx
  export function FieldInput(props: {
    field: EntityField;
    value: unknown;
    onChange: (value: unknown) => void;
    error?: string;
  }): JSX.Element | null; // null for kind: "id"
  ```
  Task 3 (`GeneratedForm`) consumes all three.

- [ ] **Step 1: Add `@mantine/dates`**

In `web/`, run: `pnpm add @mantine/dates`. `@mantine/dates` declares `dayjs` as a peer dependency (`>=1.0.0`) that isn't already present in `web/`'s dependency tree — run `pnpm add dayjs` too, or `pnpm build`/the dev server will fail to resolve it at runtime.

- [ ] **Step 2: Extend `ApiError` with `fieldErrors`**

In `web/src/platform/api/client.ts`, update:

```ts
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fieldErrors?: Record<string, string[]>;

  constructor(status: number, code: string, message: string, fieldErrors?: Record<string, string[]>) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fieldErrors = fieldErrors;
  }
}
```

Update `ErrorBody` and the `apiFetch` throw site:

```ts
type ErrorBody = {
  error: {
    code: string;
    message: string;
    requestId: string;
    traceId: string;
    fieldErrors?: Record<string, string[]>;
  };
};
```

```ts
    if (body?.error) {
      throw new ApiError(response.status, body.error.code, body.error.message, body.error.fieldErrors);
    }
```

- [ ] **Step 3: Write `useApiMutation.ts`**

Create `web/src/platform/api/useApiMutation.ts`:

```ts
import { useMutation } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch, type ApiError } from "./client";

export function useApiMutation<TResponse, TBody = unknown>(
  method: "POST" | "PATCH",
  path: string,
) {
  const { token } = useAuth();

  return useMutation<TResponse, ApiError, TBody>({
    mutationFn: (body: TBody) =>
      apiFetch<TResponse>(path, token, {
        method,
        body: JSON.stringify(body),
      }),
  });
}
```

- [ ] **Step 4: Write `FieldInput.tsx`**

Create `web/src/platform/field/FieldInput.tsx`:

```tsx
import { Checkbox, NumberInput, Select, Textarea, TextInput } from "@mantine/core";
import { DateInput, DateTimePicker } from "@mantine/dates";
import type { EntityField } from "../metadata/types";

export function FieldInput({
  field,
  value,
  onChange,
  error,
}: {
  field: EntityField;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
}) {
  const label = field.label + (field.required ? " *" : "");

  switch (field.kind) {
    case "id":
      return null;
    case "boolean":
      return (
        <Checkbox
          label={label}
          checked={Boolean(value)}
          onChange={(event) => onChange(event.currentTarget.checked)}
          error={error}
        />
      );
    case "number":
    case "money":
      return (
        <NumberInput
          label={label}
          value={typeof value === "number" ? value : ""}
          onChange={(v) => onChange(typeof v === "number" ? v : undefined)}
          error={error}
        />
      );
    case "date":
      return (
        <DateInput
          label={label}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "datetime":
      return (
        <DateTimePicker
          label={label}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "enum":
      return (
        <Select
          label={label}
          data={(field.enumValues ?? []).map((v) => ({ value: v, label: v }))}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "json":
      return (
        <Textarea
          label={label}
          value={typeof value === "string" ? value : value ? JSON.stringify(value) : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
        />
      );
    case "string":
    case "reference":
      return (
        <TextInput
          label={label}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
        />
      );
  }
}
```

Note: `json`'s `onChange` stores the raw textarea string as-is (not `JSON.parse`d) — `GeneratedForm` (Task 3) is responsible for parsing it into real JSON right before submit, catching a parse error as a client-side field error before the request is sent (see Task 3, Step 1).

Note: `@mantine/dates@9.5.0`'s `DateInput`/`DateTimePicker` both take `value?: DateValue | Date` (read-side, accepts a `Date`) but `onChange?: (value: DateStringValue | null) => void` (write-side — always a string, e.g. `"2026-08-02"`, never a `Date`) — verified against the installed package's `.d.ts` during implementation. The code above already reflects this (string in, string out); don't reintroduce a `.toISOString()` call, `onChange` never hands you a `Date` to call it on.

- [ ] **Step 5: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/pnpm-lock.yaml web/src/platform/api/client.ts web/src/platform/api/useApiMutation.ts web/src/platform/field/FieldInput.tsx
git commit -m "Add useApiMutation, FieldInput, and @mantine/dates for GeneratedForm"
```

---

### Task 3: `GeneratedForm`

**Files:**
- Create: `web/src/platform/form/GeneratedForm.tsx`
- Modify: `web/src/App.tsx` (wire a route to exercise it manually)

**Interfaces:**
- Consumes: `FieldInput`, `useApiMutation`, `ApiError` (Task 2); `useEntity`, `useApiQuery` (existing).

- [ ] **Step 1: Write `GeneratedForm.tsx`**

Create `web/src/platform/form/GeneratedForm.tsx`:

```tsx
import { useEffect, useState } from "react";
import { Alert, Button, Container, Stack, Title } from "@mantine/core";
import { useApiQuery } from "../api/useApiQuery";
import { useApiMutation } from "../api/useApiMutation";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntity } from "../metadata/useEntity";
import { FieldInput } from "../field/FieldInput";

type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
};

export function GeneratedForm({
  entityName,
  recordId,
  onSaved,
}: {
  entityName: string;
  recordId?: string;
  onSaved: (record: RecordDto) => void;
}) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  const {
    data: existing,
    isLoading: existingLoading,
    error: existingError,
  } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", entityName, recordId],
    `/api/${entityName}/${recordId}`,
    (response) => response.data,
    Boolean(recordId),
  );

  const [formData, setFormData] = useState<Record<string, unknown>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string[]>>({});

  useEffect(() => {
    if (existing) {
      setFormData(existing.data);
    }
  }, [existing]);

  const createMutation = useApiMutation<{ data: RecordDto }, { data: Record<string, unknown> }>(
    "POST",
    `/api/${entityName}`,
  );
  const updateMutation = useApiMutation<
    { data: RecordDto },
    { version: number; data: Record<string, unknown> }
  >("PATCH", `/api/${entityName}/${recordId}`);

  if (entityLoading || (recordId && existingLoading)) {
    return <div>Loading...</div>;
  }
  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }
  if (recordId && existingError) {
    return <ApiErrorMessage error={existingError} />;
  }
  if (!entity) {
    return <div>Entity not found.</div>;
  }

  function setFieldValue(fieldName: string, value: unknown) {
    setFormData((prev) => ({ ...prev, [fieldName]: value }));
  }

  async function handleSubmit() {
    setFormError(null);
    setFieldErrors({});

    const payload: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(formData)) {
      const field = entity!.fields.find((f) => f.name === key);
      if (field?.kind === "json" && typeof value === "string") {
        try {
          payload[key] = value.trim() === "" ? undefined : JSON.parse(value);
        } catch {
          setFieldErrors({ [key]: ["Invalid JSON"] });
          return;
        }
      } else {
        payload[key] = value;
      }
    }

    try {
      const response = recordId
        ? await updateMutation.mutateAsync({ version: existing!.version, data: payload })
        : await createMutation.mutateAsync({ data: payload });
      onSaved(response.data);
    } catch (error) {
      if (error instanceof ApiError) {
        setFieldErrors(error.fieldErrors ?? {});
        if (!error.fieldErrors) {
          setFormError(error.message);
        }
      } else {
        setFormError("Something went wrong.");
      }
    }
  }

  const submitting = createMutation.isPending || updateMutation.isPending;

  return (
    <Container py="xl" size="sm">
      <Title order={2} mb="md">
        {recordId ? `Edit ${entity.label}` : `New ${entity.label}`}
      </Title>
      {formError ? (
        <Alert color="red" mb="md">
          {formError}
        </Alert>
      ) : null}
      <Stack>
        {entity.fields
          .filter((field) => field.kind !== "id")
          .map((field) => (
            <FieldInput
              key={field.name}
              field={field}
              value={formData[field.name]}
              onChange={(value) => setFieldValue(field.name, value)}
              error={fieldErrors[field.name]?.join(", ")}
            />
          ))}
        <Button onClick={() => void handleSubmit()} loading={submitting}>
          Save
        </Button>
      </Stack>
    </Container>
  );
}
```

- [ ] **Step 2: Wire a route to manually exercise it**

In `web/src/App.tsx`, add a route for creating a new record, reusing the existing `RequireAuth` wrapper:

```tsx
<Route
  path="/records/:entityName/new"
  element={
    <RequireAuth>
      <NewRecordRoute />
    </RequireAuth>
  }
/>
```

with a small route component alongside the existing `RecordsRoute`:

```tsx
function NewRecordRoute() {
  const { entityName } = useParams<{ entityName: string }>();
  const navigate = useNavigate();

  if (!entityName) {
    return <div>Missing entity name.</div>;
  }

  return (
    <GeneratedForm
      entityName={entityName}
      onSaved={() => navigate(`/records/${entityName}`)}
    />
  );
}
```

(Add `useNavigate` to the existing `react-router-dom` import line, and import `GeneratedForm` from `./platform/form/GeneratedForm`.) This route is enough to manually verify create works end-to-end; wiring an "Edit" link into `GeneratedList` and a full navigation flow is a nice-to-have, not required for this sub-project's scope — the component itself is the deliverable.

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Manual browser verification**

Start `docker compose up -d postgres rabbitmq` (if not up), `pnpm dev` (API, repo root), `cd web && pnpm dev` (frontend). Log in via `/dev-login` with a minted token, navigate to `/records/crm.customers/new`, and confirm:
- All non-`id` fields render with the expected widget (text/number/checkbox/select/date picker).
- Submitting with an empty `code`/`name` shows the backend's field-level error next to the right input (proves Task 1's `fieldErrors` plumbing end-to-end).
- Submitting valid data creates the record and navigates back to the list, where it appears (proves the create path end-to-end, including `FieldValue` from sub-project 1 rendering the new row).
- Navigating to `/records/crm.customers` (list), noting an existing record's id, then manually hitting the form again with that data pre-filled isn't wired to a UI link yet (per Step 2's scope note) — if edit needs verifying, temporarily add `?recordId=<id>` handling or test `GeneratedForm` with a hardcoded `recordId` prop locally; remove any such temporary test wiring before considering this step done.

- [ ] **Step 5: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing — this task touches no backend code in Task 2/3, but confirms nothing else was disturbed.

- [ ] **Step 6: Commit**

```bash
git add web/src/platform/form/GeneratedForm.tsx web/src/App.tsx
git commit -m "Add GeneratedForm and wire a /records/:entityName/new route"
```
