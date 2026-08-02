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
                  variant={
                    state === currentState ? "filled" : terminalStates.has(state) ? "outline" : "light"
                  }
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
