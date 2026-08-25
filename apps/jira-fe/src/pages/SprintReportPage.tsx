import { useEffect, useMemo, useState } from "react";
import { Card, Container, Group, Progress, Select, Stack, Text, Title } from "@mantine/core";
import { useQueries } from "@tanstack/react-query";
import { apiFetch, useApiQuery, useAuth } from "@metap/platform-react";
import type { IssueRecord, ListResponse, ProjectRecord, SprintRecord } from "../api/types";

type WorkflowEvent = {
  id: string;
  entity: string;
  record_id: string;
  action: string;
  from_state: string;
  to_state: string;
  actor: string | null;
  created_at: string;
};

function toDateOnly(iso: string): Date {
  const d = new Date(iso);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

function enumerateDays(start: Date, end: Date): Date[] {
  const days: Date[] = [];
  for (let d = new Date(start); d <= end; d.setDate(d.getDate() + 1)) {
    days.push(new Date(d));
  }
  return days;
}

/** Reconstructs "what state was this issue in as of `day`" from its ordered transition history —
 *  `metap-workflow::list_events` returns rows `ORDER BY created_at ASC`, so the last event at or
 *  before `day` is the state that held for the rest of that day. `null` means the issue hadn't
 *  transitioned yet by `day` (still in its workflow's initial state, `"todo"` — never `"done"`). */
function statusAsOf(events: WorkflowEvent[], day: Date): string | null {
  let status: string | null = null;
  for (const event of events) {
    if (toDateOnly(event.created_at) <= day) {
      status = event.to_state;
    } else {
      break;
    }
  }
  return status;
}

function BurndownChart({
  days,
  idealSeries,
  actualSeries,
  totalPoints,
}: {
  days: Date[];
  idealSeries: number[];
  actualSeries: (number | null)[];
  totalPoints: number;
}) {
  const width = 720;
  const height = 280;
  const padding = 44;
  const innerW = width - padding * 2;
  const innerH = height - padding * 2;
  const maxY = Math.max(totalPoints, 1);
  const n = Math.max(days.length - 1, 1);

  const x = (i: number) => padding + (innerW * i) / n;
  const y = (v: number) => padding + innerH - (innerH * v) / maxY;

  const idealPath = idealSeries.map((v, i) => `${i === 0 ? "M" : "L"} ${x(i)} ${y(v)}`).join(" ");
  const actualPoints = actualSeries
    .map((v, i) => (v === null ? null : { x: x(i), y: y(v) }))
    .filter((p): p is { x: number; y: number } => p !== null);
  const actualPath = actualPoints.map((p, i) => `${i === 0 ? "M" : "L"} ${p.x} ${p.y}`).join(" ");

  return (
    <svg width={width} height={height} role="img" aria-label="Sprint burndown chart">
      <line
        x1={padding}
        y1={padding}
        x2={padding}
        y2={height - padding}
        stroke="var(--mantine-color-gray-4)"
      />
      <line
        x1={padding}
        y1={height - padding}
        x2={width - padding}
        y2={height - padding}
        stroke="var(--mantine-color-gray-4)"
      />
      <text x={4} y={padding + 4} fontSize={11} fill="var(--mantine-color-dimmed)">
        {maxY}
      </text>
      <text x={4} y={height - padding + 4} fontSize={11} fill="var(--mantine-color-dimmed)">
        0
      </text>
      {idealPath ? (
        <path
          d={idealPath}
          fill="none"
          stroke="var(--mantine-color-gray-5)"
          strokeDasharray="6 4"
          strokeWidth={2}
        />
      ) : null}
      {actualPath ? (
        <path d={actualPath} fill="none" stroke="var(--mantine-color-blue-6)" strokeWidth={2.5} />
      ) : null}
      {actualPoints.map((p, i) => (
        <circle key={i} cx={p.x} cy={p.y} r={3} fill="var(--mantine-color-blue-6)" />
      ))}
    </svg>
  );
}

/**
 * Sprint report: completion summary + a real burndown chart, "story points remaining per day"
 * reconstructed from `GET /api/jira.issues/{id}/workflow-events` (the generic transition-history
 * route, `crates/metap-http/src/routes/workflow_events.rs`) rather than a bespoke time-series
 * table — `jira.issues.storyPoints`/`sprint`/`status` are the only jira-specific inputs, the data
 * source itself is a platform primitive any entity with a workflow gets for free.
 *
 * No day-by-day snapshot exists ahead of time — this recomputes the whole series client-side on
 * every load from the full event history, fine at demo scale (one sprint's worth of issues), not
 * meant to scale to a sprint with thousands of issues.
 */
export function SprintReportPage() {
  const { token } = useAuth();

  const { data: projects, isLoading: projectsLoading } = useApiQuery<
    ListResponse<ProjectRecord>,
    ProjectRecord[]
  >(["report-projects"], "/api/jira.projects?limit=100", (response) => response.data);

  const [projectId, setProjectId] = useState<string | null>(null);
  useEffect(() => {
    if (!projectId && projects && projects.length > 0) {
      setProjectId(projects[0]!.id);
    }
  }, [projects, projectId]);

  const { data: sprints } = useApiQuery<ListResponse<SprintRecord>, SprintRecord[]>(
    ["report-sprints", projectId],
    `/api/jira.sprints?project=${projectId}&limit=100`,
    (response) => response.data,
    projectId !== null,
  );

  const [sprintId, setSprintId] = useState<string | null>(null);
  useEffect(() => {
    setSprintId(null);
  }, [projectId]);
  useEffect(() => {
    if (!sprintId && sprints && sprints.length > 0) {
      setSprintId(sprints[0]!.id);
    }
  }, [sprints, sprintId]);

  const sprint = (sprints ?? []).find((s) => s.id === sprintId) ?? null;

  const { data: issues } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["report-issues", sprintId],
    `/api/jira.issues?sprint=${sprintId}&limit=200`,
    (response) => response.data,
    sprintId !== null,
  );

  const eventQueries = useQueries({
    queries: (issues ?? []).map((issue) => ({
      queryKey: ["workflow-events", issue.id],
      queryFn: () =>
        apiFetch<{ data: WorkflowEvent[] }>(
          `/api/jira.issues/${issue.id}/workflow-events`,
          token,
        ).then((r) => r.data),
    })),
  });
  const eventsLoaded = eventQueries.length === 0 || eventQueries.every((q) => q.isSuccess);

  const eventsByIssueId = useMemo(() => {
    const map: Record<string, WorkflowEvent[]> = {};
    (issues ?? []).forEach((issue, i) => {
      map[issue.id] = eventQueries[i]?.data ?? [];
    });
    return map;
  }, [issues, eventQueries]);

  const report = useMemo(() => {
    if (!issues || !sprint?.data.startDate || !sprint?.data.endDate) {
      return null;
    }
    const start = toDateOnly(sprint.data.startDate);
    const end = toDateOnly(sprint.data.endDate);
    const today = toDateOnly(new Date().toISOString());
    const days = enumerateDays(start, end);
    const totalPoints = issues.reduce((sum, issue) => sum + (issue.data.storyPoints ?? 0), 0);
    const idealSeries = days.map((_, i) => totalPoints * (1 - i / Math.max(days.length - 1, 1)));
    const actualSeries = days.map((day) => {
      if (day > today) return null;
      return issues.reduce((sum, issue) => {
        const points = issue.data.storyPoints ?? 0;
        const status = statusAsOf(eventsByIssueId[issue.id] ?? [], day);
        return status === "done" ? sum : sum + points;
      }, 0);
    });
    const donePoints = issues.reduce(
      (sum, issue) => sum + (issue.status === "done" ? (issue.data.storyPoints ?? 0) : 0),
      0,
    );
    return { days, totalPoints, idealSeries, actualSeries, donePoints };
  }, [issues, sprint, eventsByIssueId]);

  return (
    <Container size="lg" py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>Sprint Report</Title>
        <Group>
          <Select
            w={240}
            placeholder="Select a project"
            data={(projects ?? []).map((p) => ({
              value: p.id,
              label: `${p.data.key} — ${p.data.name}`,
            }))}
            value={projectId}
            onChange={setProjectId}
            disabled={projectsLoading}
          />
          <Select
            w={240}
            placeholder="Select a sprint"
            data={(sprints ?? []).map((s) => ({ value: s.id, label: s.data.name }))}
            value={sprintId}
            onChange={setSprintId}
            disabled={!projectId}
          />
        </Group>
      </Group>

      {!sprintId ? <Text c="dimmed">No sprint selected.</Text> : null}

      {sprintId && issues && report === null ? (
        <Text c="dimmed">This sprint has no start/end date set — can't compute a burndown.</Text>
      ) : null}

      {sprintId && report ? (
        <>
          <Card withBorder mb="md" padding="md">
            <Stack gap={4}>
              <Text size="sm">
                {sprint?.data.startDate} → {sprint?.data.endDate}
              </Text>
              <Text size="sm">
                Total story points: <strong>{report.totalPoints}</strong>
              </Text>
              <Text size="sm">
                Done: <strong>{report.donePoints}</strong> (
                {report.totalPoints > 0
                  ? Math.round((report.donePoints / report.totalPoints) * 100)
                  : 0}
                %)
              </Text>
              <Progress
                value={report.totalPoints > 0 ? (report.donePoints / report.totalPoints) * 100 : 0}
                mt={4}
              />
            </Stack>
          </Card>

          <Card withBorder padding="md">
            <Title order={4} mb="sm">
              Burndown
            </Title>
            {!eventsLoaded ? (
              <Text size="sm" c="dimmed">
                Loading transition history…
              </Text>
            ) : (
              <>
                <BurndownChart
                  days={report.days}
                  idealSeries={report.idealSeries}
                  actualSeries={report.actualSeries}
                  totalPoints={report.totalPoints}
                />
                <Group gap="lg" mt="xs">
                  <Group gap={6}>
                    <span
                      style={{
                        width: 14,
                        height: 2.5,
                        background: "var(--mantine-color-blue-6)",
                        display: "inline-block",
                      }}
                    />
                    <Text size="xs" c="dimmed">
                      Actual remaining
                    </Text>
                  </Group>
                  <Group gap={6}>
                    <span
                      style={{
                        width: 14,
                        height: 0,
                        borderTop: "2px dashed var(--mantine-color-gray-5)",
                        display: "inline-block",
                      }}
                    />
                    <Text size="xs" c="dimmed">
                      Ideal
                    </Text>
                  </Group>
                </Group>
              </>
            )}
          </Card>
        </>
      ) : null}
    </Container>
  );
}
