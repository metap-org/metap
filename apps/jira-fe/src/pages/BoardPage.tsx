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
import { apiFetch, ApiErrorMessage, useApiQuery, useAuth, useEntity } from "@metap/platform-react";
import type { IssueRecord, ListResponse, ProjectRecord } from "../api/types";

const FALLBACK_STATUS_ORDER = ["todo", "in_progress", "in_review", "done"];

const STATUS_LABEL: Record<string, string> = {
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
};

const PRIORITY_COLOR: Record<string, string> = {
  urgent: "red",
  high: "orange",
  medium: "yellow",
  low: "gray",
};

/** The dataTransfer payload for one dragged card — just enough to find a matching transition
 *  and to run the optimistic-concurrency check the transition endpoint requires. */
type DragPayload = { id: string; version: number; status: string };

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
      <Group gap="xs" mt="xs">
        <Badge size="sm" color={PRIORITY_COLOR[issue.data.priority]} variant="light">
          {issue.data.priority}
        </Badge>
        {issue.data.dueDate ? (
          <Badge size="sm" color="gray" variant="outline">
            {issue.data.dueDate}
          </Badge>
        ) : null}
      </Group>
      {issue.data.assigneeEmail ? (
        <Text size="xs" c="dimmed" mt={4}>
          {issue.data.assigneeEmail}
        </Text>
      ) : null}
    </Card>
  );
}

export function BoardPage() {
  const { token } = useAuth();
  const queryClient = useQueryClient();

  const { data: projects, isLoading: projectsLoading } = useApiQuery<
    ListResponse<ProjectRecord>,
    ProjectRecord[]
  >(["board-projects"], "/api/jira.projects?limit=100", (response) => response.data);

  const [projectId, setProjectId] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId && projects && projects.length > 0) {
      setProjectId(projects[0]!.id);
    }
  }, [projects, projectId]);

  const { data: issueEntity } = useEntity("jira.issues");

  const statusColumns = useMemo(() => {
    const statusField = issueEntity?.fields.find(
      (f) => f.name === issueEntity.workflow?.stateField,
    );
    return statusField?.enumValues ?? FALLBACK_STATUS_ORDER;
  }, [issueEntity]);

  const issuesQueryKey = ["board-issues", projectId];
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

  const columns = useMemo(() => {
    const grouped = new Map<string, IssueRecord[]>(statusColumns.map((s) => [s, []]));
    for (const issue of issues ?? []) {
      grouped.get(issue.status)?.push(issue);
    }
    return grouped;
  }, [issues, statusColumns]);

  function handleDragStart(e: DragEvent, issue: IssueRecord) {
    const payload: DragPayload = { id: issue.id, version: issue.version, status: issue.status };
    e.dataTransfer.setData("application/json", JSON.stringify(payload));
  }

  async function handleDrop(e: DragEvent, targetStatus: string) {
    e.preventDefault();
    const raw = e.dataTransfer.getData("application/json");
    if (!raw) return;
    const payload = JSON.parse(raw) as DragPayload;

    if (payload.status === targetStatus) return;

    const transition = issueEntity?.workflow?.transitions.find(
      (t) => t.from === payload.status && t.to === targetStatus,
    );
    if (!transition) {
      notifications.show({
        color: "red",
        title: "No such transition",
        message: `An issue can't move directly from "${STATUS_LABEL[payload.status] ?? payload.status}" to "${STATUS_LABEL[targetStatus] ?? targetStatus}".`,
      });
      return;
    }

    try {
      // Same call shape `WorkflowActionBar` uses — this board is just another caller of the
      // same transition endpoint, not a parallel code path.
      await apiFetch(`/api/jira.issues/${payload.id}/transitions/${transition.action}`, token, {
        method: "POST",
        body: JSON.stringify({ version: payload.version }),
      });
      await queryClient.invalidateQueries({ queryKey: issuesQueryKey });
    } catch {
      notifications.show({
        color: "red",
        title: "Transition failed",
        message: "The issue may have changed since the board last loaded — reload and try again.",
      });
      await queryClient.invalidateQueries({ queryKey: issuesQueryKey });
    }
  }

  if (projectsLoading) return <div>Loading…</div>;

  return (
    <Container size="xl" py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>Board</Title>
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
        <SimpleGrid cols={{ base: 1, sm: 2, lg: statusColumns.length }}>
          {statusColumns.map((status) => (
            <Stack
              key={status}
              gap="xs"
              p="xs"
              style={{ background: "var(--mantine-color-gray-0)", borderRadius: 8, minHeight: 200 }}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => void handleDrop(e, status)}
            >
              <Text fw={700} size="sm">
                {STATUS_LABEL[status] ?? status} ({columns.get(status)?.length ?? 0})
              </Text>
              {(columns.get(status) ?? []).map((issue) => (
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
