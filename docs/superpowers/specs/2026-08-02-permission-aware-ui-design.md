# Permission-Aware UI State: Proactive Record Capabilities

Date: 2026-08-02

Status: approved

Scope: fourth of five planned Phase 6 (Frontend Core) sub-projects, in this priority order:

1. FieldRenderer foundation (`FieldValue`) — done
2. `GeneratedForm` — done
3. `WorkflowActionBar` — done
4. **Permission-aware UI state** (this spec)
5. Pagination / table virtualization for `GeneratedList`

Sub-projects 1-3: `docs/superpowers/specs/2026-08-02-{field-renderer,generated-form,workflow-action-bar}-design.md`, shipped in commit `8e6d78c`.

## Motivation

Today's model is entirely reactive: `GeneratedForm` only learns a field is unwritable when a submit returns `403` naming it (sub-project 2); `WorkflowActionBar` only learns a transition's guard fails when the click itself returns `422` (sub-project 3). Nothing is proactively hidden or disabled — a user can always try, and sometimes fail, on something the server already knew was off-limits before they clicked.

There's no existing non-admin-gated endpoint that tells the frontend "what can the current caller do with this record" in advance — `PolicyExplainer` (`POST /admin/policies/explain`) is admin-gated by design and wrong to repurpose for ordinary end-user runtime UI hints. This sub-project adds that missing capability, scoped to what a single record's detail view needs.

## Design

### Backend: capabilities bundled into `GET /api/:entity/:id`

No new endpoint — `CrudService.get()` (already loads the record, the entity, and a `PermissionSnapshot` for masking) computes capabilities from that same data and adds them to its existing response, so there's no extra round-trip.

**New `PermissionSnapshot.writableFields`** (`src/core/permission/permission-snapshot.ts`) — the "give me everything allowed" counterpart to the existing "fail on the first thing that isn't" `assertWritableFields`:

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
```

`assertWritableFields` is refactored to be built on top of this (same policy-grouping logic, not duplicated):

```ts
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

Behavior-preserving: both iterate `payloadFields` in order and stop at the first denial, matching today's existing `assertWritableFields` tests.

**`CrudService.get()`** computes and attaches capabilities using pieces that already exist — `writableFields` above, the existing `canUpdateRecordCondition` (record-level permission), and the existing `WorkflowEngine.runGuard` (a pure predicate — safe to evaluate read-only without performing the transition):

```ts
type TransitionAvailability = {
  action: string;
  available: boolean;
  reason?: string;
};

type RecordCapabilities = {
  writableFields: string[];
  canUpdate: boolean;
  transitions: TransitionAvailability[];
};
```

For each of `entity.workflow.transitions` whose `from` matches the record's current state: if the record-level `canUpdateRecordCondition` check fails, the transition is `available: false` with that decision's `reason`; otherwise, `WorkflowEngine.runGuard` is evaluated against the record's real data, and `available`/`reason` reflect the guard's actual result (`true` → available, a string → that string is the `reason`, exactly what a real transition attempt would return in its `422`). No entity without a workflow gets an empty `transitions: []`.

`CrudService.get()`'s return type becomes `ServiceResult<RecordDto & { capabilities: RecordCapabilities }>` — `capabilities` rides inside the existing `data` envelope, so the route handler (`src/server/routes/records.ts`) needs no change at all.

**Scope boundary:** this only applies to `get()` (single-record fetch, the detail view). `list()` does not compute per-row capabilities — expensive per-row guard evaluation for a whole page of rows isn't justified when nothing in the list UI currently needs it (out of scope, see below). `create()` (no existing record yet) also doesn't get capabilities — see below.

### Frontend: `RecordCapabilities` type + three consumers

New shared type file `web/src/platform/detail/recordCapabilities.ts` (the first genuinely shared, non-duplicated type this session — `RecordDto` itself stays locally duplicated per file, an existing, unrelated pattern this spec doesn't touch):

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

- **`FieldValue`** (sub-project 1): a field marked `required: true` in metadata whose key is absent from the record's `data` is now an unambiguous masking signal (a required field is always present in real data — the entity's Zod schema guarantees it at write time) — rendered as a distinct locked indicator instead of a plain `—`. Optional fields keep today's behavior (absent is ambiguous between "masked" and "never set" — no change, see Out of scope).
- **`GeneratedForm`** (sub-project 2), edit mode only (`recordId` present, so a `GET` already ran and returned `capabilities`): a field not in `capabilities.writableFields` renders its `FieldInput` disabled, with a short "You can't edit this field" hint, instead of waiting for a `403` on submit. Create mode is unaffected (see Out of scope).
- **`WorkflowActionBar`** (sub-project 3): its button list still starts from `transitions whose from === currentState` (unchanged), but each button's enabled/disabled state and tooltip now come from matching `capabilities.transitions` by `action` — a transition the API already knows would fail (record-level denial or a failing guard) renders disabled with that real reason shown as a tooltip, instead of enabled-and-clickable only to fail after the round-trip.

## Out of scope (deliberate, not an oversight)

- **`list()` per-row capabilities.** Not needed by any current UI, and evaluating a guard per row per page would be real, unjustified cost.
- **Capabilities for `create()` (no `recordId` yet).** Field-level write policies are frequently record-attribute conditions (e.g. "only the record's creator can write X") that can't evaluate meaningfully against a record that doesn't exist yet; a create-mode capability check would mostly degrade to "yes for everything" and add complexity for little signal. `GeneratedForm`'s create mode keeps today's fully-reactive behavior.
- **Masked-vs-empty indicator for optional fields.** Still ambiguous with the signals available (a masked optional field and a never-set optional field look identical) — would need the backend to say "this key was deliberately omitted" versus "this key was never in the data," which `filterReadableFields` doesn't distinguish today and isn't worth changing for this.
- **Caching/staleness of `capabilities` across a session.** They're only ever read fresh off the `GET` response that produced them — no separate capability cache, no invalidation logic to reason about.

## Testing

Backend (`writableFields`, `assertWritableFields`'s preserved behavior, `CrudService.get()`'s capability computation) follows this project's existing TDD convention — minimal, targeted:
- One test: `writableFields` returns only the allowed subset for a non-admin caller with a field-level write policy in place.
- One test: `get()`'s `capabilities.writableFields` excludes a field denied by write policy.
- One test: `get()`'s `capabilities.transitions` marks a transition `available: false` with the guard's real message when the guard would fail (e.g. `crm.customers`' real `activate` guard, no email set) — and `available: true` when it would succeed.
- One test: existing `assertWritableFields` tests (already in `crud-service.test.ts`/`permission-snapshot.test.ts`) continue passing unchanged, confirming the refactor is behavior-preserving.

Frontend: same as sub-projects 1-3 — `web/` has no test framework, verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check. This session has twice been unable to complete browser verification itself (sandboxed headless Chromium missing system libraries, no `sudo`) — that limitation is expected to still apply here; manual verification by the user remains the honest gap to flag, not something to claim as done without it.
