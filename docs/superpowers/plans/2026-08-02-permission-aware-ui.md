# Permission-Aware UI State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `GET /api/:entity/:id` proactively tells the caller what they can do with a record — which fields they can write, whether they can update the record at all, and which of its workflow transitions would actually succeed right now (including real guard evaluation) — so `GeneratedForm` and `WorkflowActionBar` can disable what would fail instead of letting the user find out via a `403`/`422` after the fact.

**Architecture:** No new endpoint. `CrudService.get()` (already loading the record + a `PermissionSnapshot`) computes a `capabilities` object and adds it to the existing response envelope. Backend: one new `PermissionSnapshot.writableFields()` method (the "give me every allowed field" counterpart to the existing fail-fast `assertWritableFields`), reused by a small refactor of `assertWritableFields` itself; `CrudService.get()` gains a private `computeCapabilities()` that also calls the existing, side-effect-free `WorkflowEngine.runGuard()` per candidate transition. Frontend: a new shared `RecordCapabilities` type, a masked-vs-empty indicator in `FieldValue` for required fields, a `disabled`/`description` extension to `FieldInput` used by `GeneratedForm` in edit mode, and `WorkflowActionBar` cross-referencing `capabilities.transitions` to disable buttons with a real reason tooltip.

**Tech Stack:** Backend: Vitest live-DB integration tests (existing pattern in `crud-service.test.ts`/`permission-snapshot.test.ts`), Drizzle, no new dependency. Frontend: React 19, Mantine (`Tooltip`, `Badge`), no new dependency — `web/` has no test framework, verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check.

## Global Constraints

- No new dependency, backend or frontend (spec: capabilities ride inside the existing `GET /api/:entity/:id` response, no new endpoint; no icon library — use a Mantine `Badge`+`Tooltip`, not an emoji or a new icon package, matching this repo's no-emoji-in-files convention).
- `list()` and `create()` do not get capabilities — spec's explicit out-of-scope boundary. Only `CrudService.get()` changes.
- `assertWritableFields`'s existing behavior (iterate `payloadFields` in order, return the first denied field) must be preserved exactly by the refactor — the existing test in `permission-snapshot.test.ts` ("assertWritableFields rejects a payload touching a field the caller cannot write") must keep passing unchanged.
- `exactOptionalPropertyTypes: true` is on for the backend — `reason?: string` on `TransitionAvailability` must be set via conditional object spread (`...(cond ? { reason } : {})`), never `reason: undefined`.
- Backend work is TDD: write the failing test, watch it fail, then implement.
- Mantine `Button`'s `disabled` state blocks pointer events, which silently breaks a wrapping `Tooltip` unless the `Tooltip`'s child is wrapped in an intermediate element (e.g. `<span>`) — call this out explicitly in Task 5, don't rediscover it via a broken tooltip.

---

### Task 1: `PermissionSnapshot.writableFields`

**Files:**
- Modify: `src/core/permission/permission-snapshot.ts`
- Test: `src/core/permission/permission-snapshot.test.ts`

**Interfaces:**
- Produces: `PermissionSnapshot.writableFields(context: RequestContext, allFieldNames: readonly string[], existingRecord: Record<string, unknown> | undefined): string[]` — Task 3 (`CrudService.get()`) consumes this.
- `assertWritableFields`'s existing signature and behavior are unchanged from the caller's perspective (`src/core/crud/crud-service.ts`'s `create`/`update` calls are untouched by this task).

- [ ] **Step 1: Write the failing test**

In `src/core/permission/permission-snapshot.test.ts`, add after the existing `"assertWritableFields rejects a payload touching a field the caller cannot write"` test (inside the same `describe("PermissionSnapshot (live DB)", ...)` block):

```ts
  it("writableFields returns only the fields allowed for a non-admin caller", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "write", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(db, tenantId, entity);

      const asHr = snapshot.writableFields(contextWithRoles(["hr"]), ["name", "salary"], undefined);
      expect(asHr).toEqual(["name", "salary"]);

      const asViewer = snapshot.writableFields(
        contextWithRoles(["viewer"]),
        ["name", "salary"],
        undefined,
      );
      expect(asViewer).toEqual(["name"]);
    } finally {
      await cleanup();
    }
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `pnpm vitest run src/core/permission/permission-snapshot.test.ts -t "writableFields returns only the fields allowed"`
Expected: FAIL — `snapshot.writableFields is not a function` (or a TypeScript error if run through `tsc` first; either is the expected "doesn't exist yet" failure).

- [ ] **Step 3: Implement `writableFields` and refactor `assertWritableFields` to use it**

In `src/core/permission/permission-snapshot.ts`, replace the existing `assertWritableFields` method (lines 75-111) with:

```ts
  writableFields(
    context: RequestContext,
    allFieldNames: readonly string[],
    existingRecord: Record<string, unknown> | undefined,
  ): string[] {
    if (context.roles?.includes("admin")) {
      return [...allFieldNames];
    }

    const writePoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of this.fieldPolicies) {
      if (policy.action !== "write" || !policy.field) {
        continue;
      }
      const list = writePoliciesByField.get(policy.field) ?? [];
      list.push(policy);
      writePoliciesByField.set(policy.field, list);
    }

    return allFieldNames.filter((field) => {
      const policies = writePoliciesByField.get(field);
      if (!policies || policies.length === 0) {
        return true;
      }
      return policies.some((policy) => evaluatePolicyRow(policy, context, existingRecord));
    });
  }

  assertWritableFields(
    context: RequestContext,
    payloadFields: readonly string[],
    existingRecord: Record<string, unknown> | undefined,
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const writable = new Set(this.writableFields(context, payloadFields, existingRecord));
    const deniedField = payloadFields.find((field) => !writable.has(field));

    return deniedField ? { allowed: false, reason: "forbidden", field: deniedField } : { allowed: true };
  }
```

This is behavior-preserving: both the old and new `assertWritableFields` walk `payloadFields` in order and stop at the first field whose write policies all fail; the new version just computes that via `writableFields` instead of an inline loop, so the policy-grouping logic isn't duplicated.

- [ ] **Step 4: Run the test to verify it passes**

Run: `pnpm vitest run src/core/permission/permission-snapshot.test.ts`
Expected: PASS — all tests in the file, including the new one and the pre-existing `assertWritableFields` test (confirms the refactor didn't change behavior).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add src/core/permission/permission-snapshot.ts src/core/permission/permission-snapshot.test.ts
git commit -m "Add PermissionSnapshot.writableFields, refactor assertWritableFields to use it"
```

---

### Task 2: `CrudService.get()` computes and returns `capabilities`

**Files:**
- Modify: `src/core/crud/crud-service.ts`
- Test: `src/core/crud/crud-service.test.ts`

**Interfaces:**
- Consumes: `PermissionSnapshot.writableFields` (Task 1), the existing `PermissionSnapshot.canUpdateRecordCondition`, the existing `WorkflowEngine.runGuard`.
- Produces (exported from `crud-service.ts`, both consumed by nothing backend-side beyond `get()` itself, but exported so a future caller — or a test — can name the type):
  ```ts
  export type TransitionAvailability = {
    action: string;
    available: boolean;
    reason?: string;
  };

  export type RecordCapabilities = {
    writableFields: string[];
    canUpdate: boolean;
    transitions: TransitionAvailability[];
  };
  ```
  `CrudService.get()`'s return type becomes `Promise<ServiceResult<RecordDto & { capabilities: RecordCapabilities }>>`. The route handler (`src/server/routes/records.ts`) needs **no change** — it already does `return { data: result.data }`, and `capabilities` now just rides inside that same `data` object.

- [ ] **Step 1: Write the failing tests**

In `src/core/crud/crud-service.test.ts`, inside the existing `describe("CrudService field/record enforcement (live DB)", ...)` block, add these three tests after the existing `"get() returns 403 for a non-admin blocked by a record-level read policy..."` test (the last one in the file):

```ts
  it("get() capabilities.writableFields excludes a field denied by write policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "write",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "G003", name: "Capability Co", phone: "555-4000" },
        adminContext,
      );
      expect(created.ok).toBe(true);
      if (!created.ok) return;
      recordId = created.data.id;

      const result = await container.crud.get("crm.customers", recordId, viewerContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.capabilities.writableFields).not.toContain("phone");
        expect(result.data.capabilities.writableFields).toContain("name");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("get() capabilities.transitions reports the real guard result, available or not", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createdNoEmail = await container.crud.create(
      "crm.customers",
      { code: "G004", name: "No Email Co" },
      adminContext,
    );
    expect(createdNoEmail.ok).toBe(true);
    if (!createdNoEmail.ok) return;

    const createdWithEmail = await container.crud.create(
      "crm.customers",
      { code: "G005", name: "Has Email Co", email: "g005@example.com" },
      adminContext,
    );
    expect(createdWithEmail.ok).toBe(true);
    if (!createdWithEmail.ok) return;

    try {
      const noEmailResult = await container.crud.get(
        "crm.customers",
        createdNoEmail.data.id,
        adminContext,
      );
      expect(noEmailResult.ok).toBe(true);
      if (noEmailResult.ok) {
        const activate = noEmailResult.data.capabilities.transitions.find(
          (t) => t.action === "activate",
        );
        expect(activate?.available).toBe(false);
        expect(activate?.reason).toBe("Email is required to activate a customer.");
      }

      const withEmailResult = await container.crud.get(
        "crm.customers",
        createdWithEmail.data.id,
        adminContext,
      );
      expect(withEmailResult.ok).toBe(true);
      if (withEmailResult.ok) {
        const activate = withEmailResult.data.capabilities.transitions.find(
          (t) => t.action === "activate",
        );
        expect(activate?.available).toBe(true);
        expect(activate?.reason).toBeUndefined();
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
        createdNoEmail.data.id,
      ]);
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
        createdWithEmail.data.id,
      ]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [createdNoEmail.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [createdWithEmail.data.id]);
    }
  });

  it("get() capabilities.canUpdate and transitions reflect a record-level update denial", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "G006", name: "Owned Co 2", email: "g006@example.com" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "update",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.get("crm.customers", created.data.id, editorContext);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.capabilities.canUpdate).toBe(false);
        const activate = result.data.capabilities.transitions.find((t) => t.action === "activate");
        expect(activate?.available).toBe(false);
        expect(activate?.reason).toBe("forbidden");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts -t "capabilities"`
Expected: FAIL — `result.data.capabilities` is `undefined` (property access on `undefined` or a TS error, since `get()` doesn't produce `capabilities` yet).

- [ ] **Step 3: Implement `computeCapabilities` and wire it into `get()`**

In `src/core/crud/crud-service.ts`, add the two exported types near the top of the file, right after the existing `RecordDto` type (after line 24):

```ts
export type TransitionAvailability = {
  action: string;
  available: boolean;
  reason?: string;
};

export type RecordCapabilities = {
  writableFields: string[];
  canUpdate: boolean;
  transitions: TransitionAvailability[];
};
```

Add a new private method to the `CrudService` class, right after `maskRecordForRead` (after line 56):

```ts
  private computeCapabilities(
    entity: EntityDefinition,
    context: RequestContext,
    snapshot: PermissionSnapshot,
    existingData: Record<string, unknown>,
  ): RecordCapabilities {
    const writableFields = snapshot.writableFields(
      context,
      entity.fields.map((field) => field.name),
      existingData,
    );

    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData);
    const canUpdate = recordDecision.allowed;

    const transitions: TransitionAvailability[] = [];
    const currentState = entity.workflow ? existingData[entity.workflow.stateField] : undefined;

    if (entity.workflow && typeof currentState === "string") {
      for (const transition of entity.workflow.transitions) {
        if (transition.from !== currentState) {
          continue;
        }

        if (!canUpdate) {
          transitions.push({
            action: transition.action,
            available: false,
            ...(recordDecision.reason ? { reason: recordDecision.reason } : {}),
          });
          continue;
        }

        const guardResult = this.workflow.runGuard(transition, existingData, context);
        transitions.push({
          action: transition.action,
          available: guardResult === true,
          ...(guardResult === true ? {} : { reason: guardResult }),
        });
      }
    }

    return { writableFields, canUpdate, transitions };
  }
```

Change `get()`'s return type and its final `return` statement (lines 138-186 in the current file):

```ts
  async get(
    entityName: string,
    id: string,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto & { capabilities: RecordCapabilities }>> {
```

(only the return type annotation changes — the body up through the `recordDecision`/`if (!recordDecision.allowed)` check for the `"read"` action is unchanged) and replace the final `return`:

```ts
    return {
      ok: true,
      data: {
        ...this.maskRecordForRead(entity, context, snapshot, existing),
        capabilities: this.computeCapabilities(entity, context, snapshot, existingData),
      },
    };
```

Note: `computeCapabilities` uses `canUpdateRecordCondition(context, existingData)` — the default `"update"` action, deliberately distinct from the `"read"`-action check `get()` already does a few lines above to decide whether to return the record at all. A caller can be allowed to *read* a record (`recordDecision` earlier in `get()`) but not *update* it (`computeCapabilities`'s own, separate check) — that's the whole point of `capabilities.canUpdate`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: PASS — all tests in the file, including the three new ones.

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 6: Run the full backend test suite as a regression check**

Run: `pnpm test`
Expected: all passing.

- [ ] **Step 7: Commit**

```bash
git add src/core/crud/crud-service.ts src/core/crud/crud-service.test.ts
git commit -m "CrudService.get(): compute and return record capabilities (writable fields, canUpdate, transition availability)"
```

---

### Task 3: Shared `RecordCapabilities` type + `FieldValue` masked-required indicator

**Files:**
- Create: `web/src/platform/detail/recordCapabilities.ts`
- Modify: `web/src/platform/field/FieldValue.tsx`

**Interfaces:**
- Produces:
  ```ts
  // recordCapabilities.ts
  export type TransitionAvailability = {
    action: string;
    available: boolean;
    reason?: string;
  };

  export type RecordCapabilities = {
    writableFields: string[];
    canUpdate: boolean;
    transitions: TransitionAvailability[];
  };
  ```
  Tasks 4 and 5 both import this.

- [ ] **Step 1: Create the shared type file**

Create `web/src/platform/detail/recordCapabilities.ts`:

```ts
export type TransitionAvailability = {
  action: string;
  available: boolean;
  reason?: string;
};

export type RecordCapabilities = {
  writableFields: string[];
  canUpdate: boolean;
  transitions: TransitionAvailability[];
};
```

- [ ] **Step 2: Update `FieldValue` to show a masked indicator for required fields**

Replace the full contents of `web/src/platform/field/FieldValue.tsx`:

```tsx
import { Badge, Tooltip } from "@mantine/core";
import type { EntityField } from "../metadata/types";
import { formatFieldValue } from "./fieldKindConfig";

export function FieldValue({ field, value }: { field: EntityField; value: unknown }) {
  if (value === null || value === undefined) {
    if (field.required) {
      return (
        <Tooltip label="You don't have permission to view this field">
          <Badge variant="outline" color="gray">
            Masked
          </Badge>
        </Tooltip>
      );
    }
    return <>—</>;
  }

  const formatted = formatFieldValue(field.kind, value) ?? "—";

  if (field.kind === "enum") {
    return <Badge variant="light">{formatted}</Badge>;
  }

  return <>{formatted}</>;
}
```

A field marked `required: true` in entity metadata is always present in a real record's `data` (the entity's Zod schema guarantees it at create/update time), so an absent value for a required field unambiguously means the read was masked, not "never set" — that's the signal this renders. Optional fields keep the existing plain `—` (still ambiguous, out of scope per the spec).

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/platform/detail/recordCapabilities.ts web/src/platform/field/FieldValue.tsx
git commit -m "Add shared RecordCapabilities type; FieldValue distinguishes masked from empty for required fields"
```

---

### Task 4: `FieldInput` disabled/description support + `GeneratedForm` edit-mode field disabling

**Files:**
- Modify: `web/src/platform/field/FieldInput.tsx`
- Modify: `web/src/platform/form/GeneratedForm.tsx`

**Interfaces:**
- Consumes: `RecordCapabilities` (Task 3).
- Produces: `FieldInput` gains a `disabled?: boolean` prop; `GeneratedForm`'s local `RecordDto` type gains `capabilities: RecordCapabilities`.

- [ ] **Step 1: Add `disabled` to `FieldInput`**

Replace the full contents of `web/src/platform/field/FieldInput.tsx`:

```tsx
import { Checkbox, NumberInput, Select, Textarea, TextInput } from "@mantine/core";
import { DateInput, DateTimePicker } from "@mantine/dates";
import type { EntityField } from "../metadata/types";

export function FieldInput({
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

  switch (field.kind) {
    case "id":
      return null;
    case "boolean":
      return (
        <Checkbox
          label={label}
          description={description}
          checked={Boolean(value)}
          onChange={(event) => onChange(event.currentTarget.checked)}
          error={error}
          disabled={disabled}
        />
      );
    case "number":
    case "money":
      return (
        <NumberInput
          label={label}
          description={description}
          value={typeof value === "number" ? value : ""}
          onChange={(v) => onChange(typeof v === "number" ? v : undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "date":
      return (
        <DateInput
          label={label}
          description={description}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "datetime":
      return (
        <DateTimePicker
          label={label}
          description={description}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "enum":
      return (
        <Select
          label={label}
          description={description}
          data={(field.enumValues ?? []).map((v) => ({ value: v, label: v }))}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "json":
      return (
        <Textarea
          label={label}
          description={description}
          value={typeof value === "string" ? value : value ? JSON.stringify(value) : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
          disabled={disabled}
        />
      );
    case "string":
    case "reference":
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
  }
}
```

- [ ] **Step 2: Wire `capabilities` through `GeneratedForm`**

In `web/src/platform/form/GeneratedForm.tsx`, add the import (after the existing `FieldInput` import):

```tsx
import type { RecordCapabilities } from "../detail/recordCapabilities";
```

Change the local `RecordDto` type (lines 10-14):

```ts
type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
  capabilities: RecordCapabilities;
};
```

Add a `writableFields` computation right after the `useEffect` block that seeds `formData` (after line 45, before `const createMutation = ...`):

```ts
  const writableFields =
    recordId && existing ? new Set(existing.capabilities.writableFields) : null;
```

Update the `FieldInput` usage inside the `.map((field) => ...)` (lines 124-131) to pass `disabled`:

```tsx
            <FieldInput
              key={field.name}
              field={field}
              value={formData[field.name]}
              onChange={(value) => setFieldValue(field.name, value)}
              error={fieldErrors[field.name]?.join(", ")}
              disabled={writableFields ? !writableFields.has(field.name) : false}
            />
```

`writableFields` is `null` in create mode (`recordId` unset, or `existing` not yet loaded) — every field stays enabled there, matching the spec's explicit scope boundary that create-mode capability checking is out of scope.

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/platform/field/FieldInput.tsx web/src/platform/form/GeneratedForm.tsx
git commit -m "GeneratedForm: proactively disable fields the caller can't write, in edit mode"
```

---

### Task 5: `WorkflowActionBar` transition availability + `RecordDetail` wiring + verification

**Files:**
- Modify: `web/src/platform/workflow/WorkflowActionBar.tsx`
- Modify: `web/src/platform/detail/RecordDetail.tsx`

**Interfaces:**
- Consumes: `RecordCapabilities`/`TransitionAvailability` (Task 3).
- `WorkflowActionBar` gains a required `capabilities: RecordCapabilities` prop.

- [ ] **Step 1: Update `WorkflowActionBar`**

In `web/src/platform/workflow/WorkflowActionBar.tsx`, add the import (after the existing `EntityWorkflow` type import):

```tsx
import type { RecordCapabilities } from "../detail/recordCapabilities";
```

Change the component's props type and destructuring (lines 47-61) to add `capabilities`:

```tsx
export function WorkflowActionBar({
  entityName,
  recordId,
  version,
  workflow,
  currentState,
  capabilities,
  onTransitioned,
}: {
  entityName: string;
  recordId: string;
  version: number;
  workflow: EntityWorkflow;
  currentState: string;
  capabilities: RecordCapabilities;
  onTransitioned: (record: RecordDto) => void;
}) {
```

Add a lookup map right after the existing `const availableTransitions = ...` line (line 68):

```tsx
  const transitionInfo = new Map(capabilities.transitions.map((t) => [t.action, t]));
```

Replace the transition-buttons `<Group>` block (lines 125-136) with:

```tsx
        <Group>
          {availableTransitions.map((transition) => {
            const info = transitionInfo.get(transition.action);
            const blocked = info ? !info.available : false;

            return (
              <Tooltip
                key={transition.action}
                label={info?.reason ?? ""}
                disabled={!info || info.available}
              >
                <span>
                  <Button
                    onClick={() => void handleTransition(transition.action)}
                    loading={pendingAction === transition.action}
                    disabled={
                      blocked || (pendingAction !== null && pendingAction !== transition.action)
                    }
                  >
                    {transition.label} ({transition.from} → {transition.to})
                  </Button>
                </span>
              </Tooltip>
            );
          })}
        </Group>
```

Add `Tooltip` to the existing `@mantine/core` import (line 2):

```tsx
import { Alert, Badge, Button, Group, Stack, Text, Tooltip } from "@mantine/core";
```

Note: `Tooltip`'s child here is wrapped in a plain `<span>`, not the `Button` directly — a disabled Mantine `Button` sets `pointer-events: none`, which stops the `Tooltip`'s hover listener from ever firing if it's attached straight to the button; the `<span>` gives the tooltip a hoverable element that isn't itself disabled.

- [ ] **Step 2: Wire `capabilities` through `RecordDetail`**

In `web/src/platform/detail/RecordDetail.tsx`, add the import (after the existing `WorkflowActionBar` import):

```tsx
import type { RecordCapabilities } from "./recordCapabilities";
```

Change the local `RecordDto` type (lines 9-13):

```ts
type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
  capabilities: RecordCapabilities;
};
```

Add `capabilities={record.capabilities}` to the `<WorkflowActionBar>` usage (inside the `entity.workflow ? (...) : null` block, lines 62-73):

```tsx
      {entity.workflow ? (
        <WorkflowActionBar
          entityName={entityName}
          recordId={id}
          version={record.version}
          workflow={entity.workflow}
          currentState={stateValue(record.data[entity.workflow.stateField])}
          capabilities={record.capabilities}
          onTransitioned={() => {
            void refetch();
          }}
        />
      ) : null}
```

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Manual browser verification**

Start `docker compose up -d postgres rabbitmq` (if not already up), `pnpm dev` (API, repo root), `cd web && pnpm dev` (frontend). Log in via `/dev-login`, navigate to `/records/crm.customers/:id` for an existing customer record (or create one first via `/records/crm.customers/new`) and confirm:
- A customer with no email shows the "Activate" button disabled, with a tooltip reading "Email is required to activate a customer." on hover.
- Setting an email via the Edit link, then returning to the detail page, shows "Activate" enabled (no tooltip) and clicking it succeeds.
- On the Edit form, every field is enabled by default (no field-level write policy exists for `crm.customers` in dev data) — to see a disabled field, this step requires either seeding a field-level write policy via `container.permissions.createPolicy` in a scratch script, or accepting that the *absence* of any disabled field with no policy configured is itself the expected, correct behavior and treating the backend tests from Task 2 as the authoritative proof of the disabling logic.
- A required field with no read policy denial still renders its value normally (no false-positive "Masked" badge) — the "Masked" badge only appears when a field-level read policy actually denies a required field, which (same as above) needs a policy seeded to observe directly; absent that, this is also covered by the existing backend masking tests, not newly by this task.

This sandbox has been unable to run a headless browser all session (missing `libnspr4.so`, no passwordless `sudo`, no cached alternative) — if that's still true, report it plainly rather than claiming visual verification happened; typecheck + lint + the backend's live-DB tests (which exercise the exact same `capabilities` values the UI reads) are the actual verification for this task.

- [ ] **Step 5: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing.

- [ ] **Step 6: Commit**

```bash
git add web/src/platform/workflow/WorkflowActionBar.tsx web/src/platform/detail/RecordDetail.tsx
git commit -m "WorkflowActionBar: disable transitions the API already knows would fail, with the real reason as a tooltip"
```
