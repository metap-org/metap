import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Card, Container, Group, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { ApiErrorMessage, useApiQuery } from "@metap/platform-react";
import type { ListResponse } from "../api/types";
import { formatMinutes } from "./IssuePanels";

type WorklogRow = {
  id: string;
  data: {
    issue: string;
    authorEmail: string;
    timeSpentMinutes: number;
    workDate: string;
    description?: string;
  };
  relatedDisplay?: Record<string, string>;
};

function isoDaysAgo(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() - days);
  return d.toISOString().slice(0, 10);
}

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/**
 * Cross-issue time tracking report ("view logwork") — distinct from `WorklogsPanel` (per-issue,
 * on `IssueDetailPage`): this is every `jira.worklogs` row across a date range, grouped by
 * author, the timesheet-style view real Jira/Tempo has on top of the same per-issue log entries.
 * Filtered with the generic `?jql=` engine (`AdvancedSearchPage`'s companion) rather than a
 * bespoke date-range query param — `workDate >= ... AND workDate <= ...` is exactly what JQL's
 * range comparisons exist for.
 */
export function LogworkReportPage() {
  const [from, setFrom] = useState(() => isoDaysAgo(30));
  const [to, setTo] = useState(() => today());

  const jql = `workDate >= "${from}" AND workDate <= "${to}" ORDER BY workDate DESC`;
  const {
    data: worklogs,
    error,
    isFetching,
  } = useApiQuery<ListResponse<WorklogRow>, WorklogRow[]>(
    ["logwork-report", from, to],
    `/api/jira.worklogs?jql=${encodeURIComponent(jql)}&limit=200`,
    (response) => response.data,
  );

  const byAuthor = useMemo(() => {
    const map = new Map<string, WorklogRow[]>();
    for (const w of worklogs ?? []) {
      const list = map.get(w.data.authorEmail) ?? [];
      list.push(w);
      map.set(w.data.authorEmail, list);
    }
    return [...map.entries()]
      .map(([author, entries]) => ({
        author,
        entries,
        total: entries.reduce((sum, e) => sum + e.data.timeSpentMinutes, 0),
      }))
      .sort((a, b) => b.total - a.total);
  }, [worklogs]);

  const grandTotal = (worklogs ?? []).reduce((sum, w) => sum + w.data.timeSpentMinutes, 0);

  return (
    <Container size="lg" py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>Logwork Report</Title>
        <Group>
          <TextInput
            label="From"
            type="date"
            value={from}
            onChange={(e) => setFrom(e.currentTarget.value)}
          />
          <TextInput
            label="To"
            type="date"
            value={to}
            onChange={(e) => setTo(e.currentTarget.value)}
          />
        </Group>
      </Group>

      {error ? (
        <Card withBorder mb="md" padding="md">
          <ApiErrorMessage error={error} />
        </Card>
      ) : null}

      {worklogs ? (
        <>
          <Text mb="md">
            Total logged: <strong>{formatMinutes(grandTotal)}</strong> across {worklogs.length} entr
            {worklogs.length === 1 ? "y" : "ies"}
            {isFetching ? " (refreshing…)" : ""}
          </Text>

          <Stack gap="md">
            {byAuthor.map(({ author, entries, total }) => (
              <Card key={author} withBorder padding="md">
                <Group justify="space-between" mb="xs">
                  <Text fw={600}>{author}</Text>
                  <Text fw={600}>{formatMinutes(total)}</Text>
                </Group>
                <Table>
                  <Table.Tbody>
                    {entries.map((w) => (
                      <Table.Tr key={w.id}>
                        <Table.Td w={120}>{w.data.workDate}</Table.Td>
                        <Table.Td>
                          <Text component={Link} to={`/issues/${w.data.issue}`} size="sm">
                            {w.relatedDisplay?.issue ?? w.data.issue}
                          </Text>
                        </Table.Td>
                        <Table.Td w={100}>{formatMinutes(w.data.timeSpentMinutes)}</Table.Td>
                        <Table.Td c="dimmed">{w.data.description}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Card>
            ))}
            {byAuthor.length === 0 ? (
              <Text c="dimmed">No work logged in this date range.</Text>
            ) : null}
          </Stack>
        </>
      ) : null}
    </Container>
  );
}
