# WorkflowActionBar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A record-detail page (`/records/:entityName/:id`) that shows a record's fields read-only and, for entities with a workflow, a `WorkflowActionBar` visualizing the state graph and offering transition buttons.

**Architecture:** Pure frontend, no backend changes (`POST /api/:entity/:id/transitions/:action` and its error shapes already exist in full). A new `RecordDetail` component (data-fetching + composition), a new `WorkflowActionBar` component (BFS-by-level state graph + transition buttons, no new dependency), and a mirrored `EntityWorkflow`/`WorkflowTransition` type replacing the current `workflow?: unknown`.

**Tech Stack:** React 19, Mantine, TanStack Query. No new dependency. `web/` has no test framework — verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check.

## Global Constraints

- No new frontend dependency (BFS-by-level layout via plain data structures + flexbox, not a graph library).
- `WorkflowActionBar` never sees `WorkflowTransition.guard` — it's a function, stripped by JSON serialization before the client ever receives it. Whether a guard passes is only knowable by attempting the transition.
- `RecordDetail`'s route-level wrapper does its own-params validation and early return *before* rendering a child that unconditionally calls data-fetching hooks — mirrors the existing `NewRecordRoute`/`GeneratedForm` split, avoids conditionally skipping hook calls inside one component.
- Minimal, targeted work — no test framework added for `web/`, matches sub-projects 1-2.

---

### Task 1: Type mirror + `WorkflowActionBar`

**Files:**
- Modify: `web/src/platform/metadata/types.ts`
- Create: `web/src/platform/workflow/WorkflowActionBar.tsx`

**Interfaces:**
- Produces:
  ```ts
  // types.ts
  export type WorkflowTransition = { action: string; from: string; to: string; label: string };
  export type EntityWorkflow = {
    stateField: string;
    initialState: string;
    terminalStates: readonly string[];
    transitions: readonly WorkflowTransition[];
  };
  // EntitySummary.workflow?: EntityWorkflow (was: unknown)

  // WorkflowActionBar.tsx
  export function WorkflowActionBar(props: {
    entityName: string;
    recordId: string;
    version: number;
    workflow: EntityWorkflow;
    currentState: string;
    onTransitioned: (record: { id: string; version: number; data: Record<string, unknown> }) => void;
  }): JSX.Element;
  ```
  Task 2 (`RecordDetail`) consumes both.

- [ ] **Step 1: Update `types.ts`**

In `web/src/platform/metadata/types.ts`, add after `EntityField`/before `EntitySummary`:

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
```

Change `EntitySummary`:

```ts
export type EntitySummary = {
  name: string;
  label: string;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: EntityWorkflow;
  version?: string;
};
```

- [ ] **Step 2: Write `WorkflowActionBar.tsx`**

Create `web/src/platform/workflow/WorkflowActionBar.tsx`:

```tsx
import { useState } from "react";
import { Alert, Badge, Button, Group, Stack, Text } from "@mantine/core";
import { useAuth } from "../auth/AuthContext";
import { apiFetch, ApiError } from "../api/client";
import type { EntityWorkflow } from "../metadata/types";

type RecordDto = { id: string; version: number; data: Record<string, unknown> };

function computeLevels(workflow: EntityWorkflow): Map<string, number> {
  const adjacency = new Map<string, string[]>();
  for (const transition of workflow.transitions) {
    const list = adjacency.get(transition.from) ?? [];
    list.push(transition.to);
    adjacency.set(transition.from, list);
  }

  const levels = new Map<string, number>();
  levels.set(workflow.initialState, 0);
  const queue: string[] = [workflow.initialState];

  while (queue.length > 0) {
    const state = queue.shift();
    if (state === undefined) {
      break;
    }
    const level = levels.get(state) ?? 0;
    for (const next of adjacency.get(state) ?? []) {
      if (!levels.has(next)) {
        levels.set(next, level + 1);
        queue.push(next);
      }
    }
  }

  return levels;
}

function groupByLevel(levels: Map<string, number>): string[][] {
  const maxLevel = Math.max(...levels.values());
  const columns: string[][] = Array.from({ length: maxLevel + 1 }, (): string[] => []);
  for (const [state, level] of levels) {
    columns[level]?.push(state);
  }
  return columns;
}

export function WorkflowActionBar({
  entityName,
  recordId,
  version,
  workflow,
  currentState,
  onTransitioned,
}: {
  entityName: string;
  recordId: string;
  version: number;
  workflow: EntityWorkflow;
  currentState: string;
  onTransitioned: (record: RecordDto) => void;
}) {
  const { token } = useAuth();
  const [showBar, setShowBar] = useState(true);
  const [actionError, setActionError] = useState<string | null>(null);
  const [pendingAction, setPendingAction] = useState<string | null>(null);

  const columns = groupByLevel(computeLevels(workflow));
  const availableTransitions = workflow.transitions.filter((t) => t.from === currentState);
  const terminalStates = new Set(workflow.terminalStates);

  async function handleTransition(action: string) {
    setActionError(null);
    setPendingAction(action);
    try {
      const response = await apiFetch<{ data: RecordDto }>(
        `/api/${entityName}/${recordId}/transitions/${action}`,
        token,
        { method: "POST", body: JSON.stringify({ version }) },
      );
      onTransitioned(response.data);
    } catch (error) {
      setActionError(error instanceof ApiError ? error.message : "Something went wrong.");
    } finally {
      setPendingAction(null);
    }
  }

  return (
    <Stack gap="xs">
      <Button variant="subtle" size="compact-sm" onClick={() => setShowBar((v) => !v)}>
        {showBar ? "Hide workflow" : "Show workflow"}
      </Button>

      {showBar ? (
        <Group align="flex-start" gap="xl">
          {columns.map((states, index) => (
            <Stack key={index} gap="xs">
              {states.map((state) => (
                <Badge
                  key={state}
                  variant={state === currentState ? "filled" : terminalStates.has(state) ? "outline" : "light"}
                  color={state === currentState ? "blue" : terminalStates.has(state) ? "gray" : undefined}
                >
                  {state}
                </Badge>
              ))}
            </Stack>
          ))}
        </Group>
      ) : null}

      {actionError ? (
        <Alert color="red" mb="xs">
          {actionError}
        </Alert>
      ) : null}

      {availableTransitions.length === 0 ? (
        <Text size="sm" c="dimmed">
          No further actions available.
        </Text>
      ) : (
        <Group>
          {availableTransitions.map((transition) => (
            <Button
              key={transition.action}
              onClick={() => void handleTransition(transition.action)}
              loading={pendingAction === transition.action}
              disabled={pendingAction !== null && pendingAction !== transition.action}
            >
              {transition.label} ({transition.from} → {transition.to})
            </Button>
          ))}
        </Group>
      )}
    </Stack>
  );
}
```

Note: this deliberately does **not** use `useApiMutation` (unlike `GeneratedForm`) — that hook binds one fixed path per hook instance, but the transition endpoint's path varies per button (`.../transitions/${action}`), which is only known at click time, not at component-render time. Calling `apiFetch` directly here (with `useAuth()` for the token, matching what `useApiMutation` does internally) is the correct, minimal tool for a genuinely dynamic-path request — extending `useApiMutation` to support this would add complexity `GeneratedForm`'s actual usage never needed.

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/platform/metadata/types.ts web/src/platform/workflow/WorkflowActionBar.tsx
git commit -m "Add WorkflowActionBar: BFS-by-level workflow visualization + transition buttons"
```

---

### Task 2: `RecordDetail` + routes

**Files:**
- Create: `web/src/platform/detail/RecordDetail.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `FieldValue` (sub-project 1), `WorkflowActionBar` (Task 1), `useEntity`, `useApiQuery`, `ApiErrorMessage`, `GeneratedForm` (sub-project 2, for the edit route).

- [ ] **Step 1: Write `RecordDetail.tsx`**

Create `web/src/platform/detail/RecordDetail.tsx`:

```tsx
import { Anchor, Container, Stack, Text, Title } from "@mantine/core";
import { Link } from "react-router-dom";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntity } from "../metadata/useEntity";
import { FieldValue } from "../field/FieldValue";
import { WorkflowActionBar } from "../workflow/WorkflowActionBar";

type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
};

function stateValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function RecordDetail({ entityName, id }: { entityName: string; id: string }) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  const {
    data: record,
    isLoading: recordLoading,
    error: recordError,
    refetch,
  } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", entityName, id],
    `/api/${entityName}/${id}`,
    (response) => response.data,
  );

  if (entityLoading || recordLoading) {
    return <div>Loading...</div>;
  }
  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }
  if (recordError) {
    return <ApiErrorMessage error={recordError} />;
  }
  if (!entity || !record) {
    return <div>Not found.</div>;
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {entity.label}
      </Title>
      <Stack mb="md">
        {entity.fields
          .filter((field) => field.kind !== "id")
          .map((field) => (
            <div key={field.name}>
              <Text size="sm" fw={500}>
                {field.label}
              </Text>
              <FieldValue field={field} value={record.data[field.name]} />
            </div>
          ))}
      </Stack>
      {entity.workflow ? (
        <WorkflowActionBar
          entityName={entityName}
          recordId={id}
          version={record.version}
          workflow={entity.workflow}
          currentState={stateValue(record.data[entity.workflow.stateField])}
          onTransitioned={() => {
            void refetch();
          }}
        />
      ) : null}
      <Anchor component={Link} to={`/records/${entityName}/${id}/edit`} mt="md" display="inline-block">
        Edit
      </Anchor>
    </Container>
  );
}
```

Note: `stateValue` exists because TypeScript's `typeof` narrowing doesn't propagate across two separate computed-property accesses on the same expression (`record.data[entity.workflow.stateField]` accessed once in a `typeof` check and again to use the value is *not* narrowed the second time, unlike narrowing a plain variable) — confirmed via `tsc` during implementation. Routing the value through a small typed function sidesteps that entirely, and also avoids a `no-base-to-string` lint error from calling `String()` on an `unknown`.

- [ ] **Step 2: Wire the routes in `App.tsx`**

Add imports:

```tsx
import { RecordDetail } from "./platform/detail/RecordDetail";
```

Add two new route components, alongside the existing `RecordsRoute`/`NewRecordRoute`:

```tsx
function RecordDetailRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();

  if (!entityName || !id) {
    return <div>Missing entity name or id.</div>;
  }

  return <RecordDetail entityName={entityName} id={id} />;
}

function EditRecordRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();
  const navigate = useNavigate();

  if (!entityName || !id) {
    return <div>Missing entity name or id.</div>;
  }

  return (
    <GeneratedForm
      entityName={entityName}
      recordId={id}
      onSaved={() => navigate(`/records/${entityName}/${id}`)}
    />
  );
}
```

Add two new `<Route>` entries inside `<Routes>`, after the existing `/records/:entityName/new` route:

```tsx
        <Route
          path="/records/:entityName/:id"
          element={
            <RequireAuth>
              <RecordDetailRoute />
            </RequireAuth>
          }
        />
        <Route
          path="/records/:entityName/:id/edit"
          element={
            <RequireAuth>
              <EditRecordRoute />
            </RequireAuth>
          }
        />
```

react-router ranks a literal path segment (`new`) higher than a dynamic one (`:id`) at the same position, so `/records/:entityName/new` and `/records/:entityName/:id` coexist without ambiguity regardless of declaration order — matches how `/records/:entityName` and `/records/:entityName/new` already coexist today.

- [ ] **Step 3: Typecheck and lint**

Run: `cd web && pnpm build && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Manual browser verification**

Start `docker compose up -d postgres rabbitmq` (if not up), `pnpm dev` (API, repo root), `cd web && pnpm dev` (frontend). Log in via `/dev-login`, create a `crm.customers` record via `/records/crm.customers/new`, then navigate to `/records/crm.customers/:id` (the id from the create response, or click through from the list once a list→detail link exists — none does yet, this is a direct-URL check) and confirm:
- Fields render read-only via `FieldValue`.
- The workflow bar shows `draft → active → blocked` with `draft` highlighted.
- Clicking "Activate" without an email set shows the real guard message ("Email is required to activate a customer.").
- Setting an email via the Edit link, then retrying "Activate", succeeds and the bar now highlights `active` with a "Block" button available.
- The "Hide workflow" toggle actually hides/shows the bar.

- [ ] **Step 5: Run the full root test suite once, as a regression check**

Run (repo root): `pnpm test`
Expected: all passing — this task touches no backend code.

- [ ] **Step 6: Commit**

```bash
git add web/src/platform/detail/RecordDetail.tsx web/src/App.tsx
git commit -m "Add RecordDetail + workflow-aware detail/edit routes"
```
