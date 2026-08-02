# WorkflowActionBar: Workflow Visualization + Transition Actions

Date: 2026-08-02

Status: approved

Scope: third of five planned Phase 6 (Frontend Core) sub-projects, in this priority order:

1. FieldRenderer foundation (`FieldValue`) — done, `docs/superpowers/specs/2026-08-02-field-renderer-design.md`
2. `GeneratedForm` — done, `docs/superpowers/specs/2026-08-02-generated-form-design.md`
3. **`WorkflowActionBar`** (this spec)
4. Permission-aware UI state
5. Pagination / table virtualization for `GeneratedList`

No backend changes — confirmed during brainstorming: `POST /api/:entity/:id/transitions/:action` and its full error shape (`404`/`403`/`400 no_workflow`/`409 invalid_transition`/`409 version_conflict`/`422 guard_failed` with a human-readable `message`) already exist from earlier phases. This sub-project is frontend-only.

## Motivation

`GeneratedList` (view) and `GeneratedForm` (create/edit) exist; there is still no way to see a single record on its own or trigger a workflow transition (`activate`, `block`, etc. — metadata-driven per entity) from the UI at all. There's also no record-detail route yet to mount either capability on.

## Design

### Route: `/records/:entityName/:id`

New `RecordDetailRoute` in `web/src/App.tsx`, alongside the existing `RecordsRoute`/`NewRecordRoute`. Fetches the record via `GET /api/:entity/:id` (built earlier this session) and the entity's metadata via the existing `useEntity`. Renders:

- Each non-`id` field via `FieldValue` (read-only) — reuses sub-project 1 as-is.
- `WorkflowActionBar` below the fields, only if `entity.workflow` is present (an entity with no workflow renders no action bar).
- An "Edit" link to `/records/:entityName/:id/edit`, which mounts the existing `GeneratedForm` with `recordId` set (a new route, but zero new component code — `GeneratedForm` already supports this via its existing `recordId` prop).

### Type mirror: `EntityWorkflow`/`WorkflowTransition`

`web/src/platform/metadata/types.ts`'s `EntitySummary.workflow?: unknown` becomes:

```ts
export type WorkflowTransition = {
  action: string;
  from: string;
  to: string;
  label: string;
};

export type EntityWorkflow = {
  stateField: string;
  initialState: string;
  terminalStates: readonly string[];
  transitions: readonly WorkflowTransition[];
};

export type EntitySummary = {
  // ...existing fields
  workflow?: EntityWorkflow;
};
```

No `guard` field — the backend's `WorkflowTransition.guard` is a function, stripped by JSON serialization before it ever reaches the client (confirmed against the existing `metadata-compiler.ts` comment: "guard functions are intentionally excluded ... already stripped on the wire"). The frontend never sees it and can never evaluate a guard client-side — only the transition attempt itself (a real request) reveals whether a guard passes, via a `422 guard_failed` response if it doesn't.

### `WorkflowActionBar`: layered state graph + transition buttons

`web/src/platform/workflow/WorkflowActionBar.tsx`. One component covering both the visual "start → end" bar and the action buttons, since both read the same `entity.workflow` + the record's current state.

**Layout algorithm** (no new graph-layout dependency — plain BFS + flexbox):

1. Build an adjacency map from `transitions`: `from -> Set<to>`.
2. BFS from `workflow.initialState`, assigning each reachable state a `level` (initialState = 0, each hop = level + 1). A state reachable via multiple paths keeps the *first* (shallowest) level it's discovered at — standard BFS behavior, keeps the layout a simple left-to-right progression even when the underlying graph has multiple paths into the same state.
3. Group states by level; render one column per level, left to right. A level with more than one state (a branch) stacks its states vertically within that column.
4. The record's current state (`data[workflow.stateField]`) is visually highlighted (a filled/bordered badge) among its column; every other state is a plain badge. A state in `workflow.terminalStates` gets a distinct visual marker (muted style, no outgoing-arrow affordance) regardless of column.

This is intentionally simple — a real graph-layout library (react-flow, dagre) is not justified for what's fundamentally a short, mostly-linear business workflow (the one real entity, `crm.customers`, is a 3-state linear chain); the BFS-by-level approach degrades gracefully to a straight left-to-right line for linear workflows and still shows branches reasonably for entities that have them, without a new dependency.

**Collapse toggle:** a small "Hide workflow" / "Show workflow" text button above the bar, backed by local `useState` (default expanded) — not a prop the embedding page controls. Purely a display preference for whoever's looking at the page.

**Action buttons:** one per `transitions` entry where `transition.from === record's current state`, labeled `"${transition.label} (${transition.from} → ${transition.to})"`. This check is purely data-driven — it does not consult `terminalStates` at all, just whether any transition's `from` matches — so it renders correctly regardless of whether an entity's `terminalStates` list is perfectly in sync with which states truly have no outgoing transitions (`MetadataCompiler` doesn't cross-validate the two today). If there are no matching transitions, render a message ("No further actions available.") instead of an empty button row.

**On click:** `useApiMutation("POST", `/api/${entityName}/${recordId}/transitions/${action}`)` with body `{ version }` (the record's current `version`, passed in as a prop from the detail route, which already fetched the record). On success, calls an `onTransitioned` callback (mirrors `GeneratedForm`'s `onSaved` — the caller's concern, not this component's) so the detail route can refetch/update. On failure:
- `422 guard_failed` — the `ApiError.message` *is* the guard's real, human-designed rejection reason (e.g. `crm.customers`' actual guard: `"Email is required to activate a customer."`) — shown directly as an inline error near the button row, not swallowed into a generic message.
- Any other error status — shown the same way, via `error.message` (already a real, server-composed message for every `CrudService.transition()` failure code, per `SERVICE_ERROR_MESSAGES` in `src/server/error-handler.ts`).

## Out of scope (deliberate, not an oversight)

- **A real graph-layout library.** BFS-by-level is enough for realistic business workflows; revisit only if an entity's workflow genuinely needs a layout BFS-by-level can't represent well (e.g. a state reachable at meaningfully different "real" depths depending on path — no such entity exists yet).
- **Optimistic UI for transitions** (updating the displayed state before the server confirms). The record detail route just refetches after `onTransitioned` — simpler, and correctness matters more than perceived latency for a state change with real business guards attached.
- **Editing while viewing the workflow bar.** The detail route is read-only + transitions; editing field values is `GeneratedForm`'s job, reached via the separate "Edit" link.

## Testing

Same as sub-projects 1 and 2 — `web/` has no test framework configured. Verification is `tsc -b`/`pnpm build` + `oxlint` (`web/`'s own, authoritative for `web/`) + a manual browser check: view a `crm.customers` record in `draft` status, confirm the bar shows draft→active→blocked with `draft` highlighted, click "Activate" without an email set and confirm the real guard message appears, add an email and retry successfully, confirm the bar now highlights `active` and offers "Block".
