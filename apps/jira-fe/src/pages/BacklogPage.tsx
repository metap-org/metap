import { useEffect, useMemo, useState } from "react";
import type { DragEvent } from "react";
import {
  Badge,
  Card,
  Container,
  Group,
  Select,
  SimpleGrid,
  Stack,
  Text,
  Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiFetch, ApiErrorMessage, useApiQuery, useAuth } from "@metap/platform-react";
import type { IssueRecord, ListResponse, ProjectRecord, SprintRecord } from "../api/types";

const PRIORITY_COLOR: Record<string, string> = {
  urgent: "red",
  high: "orange",
  medium: "yellow",
  low: "gray",
};

/** `null` id = the "Backlog" column (no sprint assigned yet) — everything else is a real sprint. */
type Column = { id: string | null; label: string };

type DragPayload = { id: string; version: number };

function IssueCard({
  issue,
  onDragStart,
}: {
  issue: IssueRecord;
  onDragStart: (e: DragEvent) => void;
}) {
  return (
    <Card withBorder padding="sm" draggable onDragStart={onDragStart} style={{ cursor: "grab" }}>
      <Text size="sm" fw={600} component={Link} to={`/issues/${issue.id}`}>
        {issue.data.title}
      </Text>
      <Badge size="sm" color={PRIORITY_COLOR[issue.data.priority]} variant="light" mt="xs">
        {issue.data.priority}
      </Badge>
    </Card>
  );
}

/**
 * Sprint planning — distinct from `BoardPage` (columns by workflow `status`, kanban-style): here
 * columns are by `sprint` assignment, a plain field, not a workflow, so moving a card between
 * columns is a normal partial `PATCH` (`CrudService::update` merges `raw_data` into existing
 * `data`, verified live — it does not require every field), not a transition call.
 *
 * `?sprint=` (empty value) meaning "no sprint assigned" only works since
 * `crates/metap-query/src/query_planner.rs`'s empty-filter-value fix (found live building this
 * page — an empty value used to 500 on a `uuid`-cast field instead of meaning "IS NULL").
 */
export function BacklogPage() {
  const { token } = useAuth();
  const queryClient = useQueryClient();

  const { data: projects, isLoading: projectsLoading } = useApiQuery<
    ListResponse<ProjectRecord>,
    ProjectRecord[]
  >(["backlog-projects"], "/api/jira.projects?limit=100", (response) => response.data);

  const [projectId, setProjectId] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId && projects && projects.length > 0) {
      setProjectId(projects[0]!.id);
    }
  }, [projects, projectId]);

  const { data: sprints } = useApiQuery<ListResponse<SprintRecord>, SprintRecord[]>(
    ["backlog-sprints", projectId],
    `/api/jira.sprints?project=${projectId}&limit=100`,
    (response) => response.data.filter((s) => s.status !== "completed"),
    projectId !== null,
  );

  const issuesQueryKey = ["backlog-issues", projectId];
  const {
    data: issues,
    isLoading: issuesLoading,
    error: issuesError,
  } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    issuesQueryKey,
    `/api/jira.issues?project=${projectId}&limit=200`,
    (response) => response.data,
    projectId !== null,
  );

  const columns: Column[] = useMemo(
    () => [
      { id: null, label: "Backlog" },
      ...(sprints ?? []).map((s) => ({ id: s.id, label: s.data.name })),
    ],
    [sprints],
  );

  const grouped = useMemo(() => {
    const map = new Map<string | null, IssueRecord[]>(columns.map((c) => [c.id, []]));
    for (const issue of issues ?? []) {
      const key = issue.data.sprint ?? null;
      (map.get(key) ?? map.get(null))!.push(issue);
    }
    return map;
  }, [issues, columns]);

  function handleDragStart(e: DragEvent, issue: IssueRecord) {
    const payload: DragPayload = { id: issue.id, version: issue.version };
    e.dataTransfer.setData("application/json", JSON.stringify(payload));
  }

  async function handleDrop(e: DragEvent, targetSprintId: string | null) {
    e.preventDefault();
    const raw = e.dataTransfer.getData("application/json");
    if (!raw) return;
    const payload = JSON.parse(raw) as DragPayload;

    try {
      await apiFetch(`/api/jira.issues/${payload.id}`, token, {
        method: "PATCH",
        body: JSON.stringify({ version: payload.version, data: { sprint: targetSprintId } }),
      });
      await queryClient.invalidateQueries({ queryKey: issuesQueryKey });
    } catch {
      notifications.show({
        color: "red",
        title: "Move failed",
        message: "The issue may have changed since the board last loaded — reload and try again.",
      });
      await queryClient.invalidateQueries({ queryKey: issuesQueryKey });
    }
  }

  if (projectsLoading) return <div>Loading…</div>;

  return (
    <Container size="xl" py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>Backlog</Title>
        <Select
          w={280}
          placeholder="Select a project"
          data={(projects ?? []).map((p) => ({
            value: p.id,
            label: `${p.data.key} — ${p.data.name}`,
          }))}
          value={projectId}
          onChange={setProjectId}
        />
      </Group>

      {issuesError ? <ApiErrorMessage error={issuesError} /> : null}
      {issuesLoading ? <div>Loading issues…</div> : null}

      {!issuesLoading && projectId ? (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: Math.min(columns.length, 4) }}>
          {columns.map((column) => (
            <Stack
              key={column.id ?? "backlog"}
              gap="xs"
              p="xs"
              style={{ background: "var(--mantine-color-gray-0)", borderRadius: 8, minHeight: 200 }}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => void handleDrop(e, column.id)}
            >
              <Text fw={700} size="sm">
                {column.label} ({grouped.get(column.id)?.length ?? 0})
              </Text>
              {(grouped.get(column.id) ?? []).map((issue) => (
                <IssueCard
                  key={issue.id}
                  issue={issue}
                  onDragStart={(e) => handleDragStart(e, issue)}
                />
              ))}
            </Stack>
          ))}
        </SimpleGrid>
      ) : null}
    </Container>
  );
}
