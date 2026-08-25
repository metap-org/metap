import { useMemo, useState } from "react";
import { Badge, Card, Container, SimpleGrid, Table, Text, TextInput, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { Link } from "react-router-dom";
import { ApiErrorMessage, BarChart, useApiQuery } from "@metap/platform-react";
import type { BarChartDatum } from "@metap/platform-react";
import type { IssueRecord, ListResponse } from "../api/types";

/**
 * `title` is `searchable` on `jira.issues` (`issue_entity.rs`) — substring/ILIKE match, so
 * `?title=<term>` already works through the generic `QueryPlanner`. No dedicated search page:
 * this box lives on the page everyone already lands on first.
 */
function SearchBox() {
  const [term, setTerm] = useState("");
  const [debounced] = useDebouncedValue(term, 300);

  const { data: results } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["search-issues", debounced],
    `/api/jira.issues?title=${encodeURIComponent(debounced)}&limit=10`,
    (response) => response.data,
    debounced.trim().length > 0,
  );

  return (
    <Card withBorder mb="xl" padding="md">
      <TextInput
        placeholder="Search issues by title…"
        value={term}
        onChange={(event) => setTerm(event.currentTarget.value)}
      />
      {debounced.trim().length > 0 ? (
        <Table mt="sm">
          <Table.Tbody>
            {(results ?? []).map((issue) => (
              <Table.Tr key={issue.id}>
                <Table.Td>
                  <Link to={`/issues/${issue.id}`}>{issue.data.title}</Link>
                </Table.Td>
                <Table.Td>
                  <Badge size="sm" variant="light">
                    {issue.status}
                  </Badge>
                </Table.Td>
              </Table.Tr>
            ))}
            {results?.length === 0 ? (
              <Table.Tr>
                <Table.Td>
                  <Text size="sm" c="dimmed">
                    No matching issues.
                  </Text>
                </Table.Td>
              </Table.Tr>
            ) : null}
          </Table.Tbody>
        </Table>
      ) : null}
    </Card>
  );
}

const STATUS_LABEL: Record<string, string> = {
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
};

const STATUS_ORDER = ["todo", "in_progress", "in_review", "done"];
const PRIORITY_ORDER = ["urgent", "high", "medium", "low"];

const PRIORITY_COLOR: Record<string, string> = {
  urgent: "red",
  high: "orange",
  medium: "yellow",
  low: "gray",
};

function countBy<T extends string>(
  issues: IssueRecord[],
  pick: (issue: IssueRecord) => T,
  order: T[],
) {
  const counts = new Map<T, number>(order.map((key) => [key, 0]));
  for (const issue of issues) {
    const key = pick(issue);
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return order.map((key) => ({ key, count: counts.get(key) ?? 0 }));
}

export function DashboardPage() {
  // Every open issue this tenant has, across every project — a real multi-project dashboard
  // would scope this, but this demo app has no project-selector-as-global-state yet (the board
  // page picks its own project locally instead, see BoardPage.tsx).
  const { data, isLoading, error } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["dashboard-issues"],
    "/api/jira.issues?limit=200",
    (response) => response.data,
  );

  const byStatus = useMemo(
    () => countBy(data ?? [], (issue) => issue.status, STATUS_ORDER),
    [data],
  );
  const byPriority = useMemo(
    () => countBy(data ?? [], (issue) => issue.data.priority, PRIORITY_ORDER),
    [data],
  );
  const recent = useMemo(() => (data ?? []).slice(0, 10), [data]);

  const statusChartData: BarChartDatum[] = byStatus.map(({ key, count }) => ({
    label: STATUS_LABEL[key] ?? key,
    value: count,
  }));
  const priorityChartData: BarChartDatum[] = byPriority.map(({ key, count }) => ({
    label: key,
    value: count,
    color: `var(--mantine-color-${PRIORITY_COLOR[key]}-6)`,
  }));

  if (isLoading) return <div>Loading…</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        Dashboard
      </Title>

      <SearchBox />

      <SimpleGrid cols={{ base: 1, sm: 2 }} mb="xl">
        <Card withBorder padding="md">
          <Text fw={600} mb="xs">
            By status
          </Text>
          <BarChart data={statusChartData} ariaLabel="Issues by status" />
        </Card>
        <Card withBorder padding="md">
          <Text fw={600} mb="xs">
            By priority
          </Text>
          <BarChart data={priorityChartData} ariaLabel="Issues by priority" />
        </Card>
      </SimpleGrid>

      <Text fw={600} mb="xs">
        Recently created
      </Text>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Title</Table.Th>
            <Table.Th>Priority</Table.Th>
            <Table.Th>Status</Table.Th>
            <Table.Th>Assignee</Table.Th>
            <Table.Th>Due</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {recent.map((issue) => (
            <Table.Tr key={issue.id}>
              <Table.Td>
                <Link to={`/issues/${issue.id}`}>{issue.data.title}</Link>
              </Table.Td>
              <Table.Td>
                <Badge color={PRIORITY_COLOR[issue.data.priority]} variant="light">
                  {issue.data.priority}
                </Badge>
              </Table.Td>
              <Table.Td>{STATUS_LABEL[issue.status] ?? issue.status}</Table.Td>
              <Table.Td>{issue.data.assigneeEmail ?? "—"}</Table.Td>
              <Table.Td>{issue.data.dueDate ?? "—"}</Table.Td>
            </Table.Tr>
          ))}
          {recent.length === 0 ? (
            <Table.Tr>
              <Table.Td colSpan={5}>
                <Text c="dimmed">No issues yet.</Text>
              </Table.Td>
            </Table.Tr>
          ) : null}
        </Table.Tbody>
      </Table>
    </Container>
  );
}
