import { useState } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  Container,
  Group,
  Table,
  Text,
  Textarea,
  Title,
} from "@mantine/core";
import { ApiErrorMessage, useApiQuery } from "@metap/platform-react";
import type { IssueRecord, ListResponse } from "../api/types";

const PRIORITY_COLOR: Record<string, string> = {
  urgent: "red",
  high: "orange",
  medium: "yellow",
  low: "gray",
};

const EXAMPLES = [
  'priority = "high" AND status != "done"',
  'issueType IN ("bug", "story") ORDER BY priority DESC',
  "storyPoints >= 5 AND assigneeEmail IS EMPTY",
  'title ~ "kanban" OR description ~ "kanban"',
];

/**
 * Advanced search over `jira.issues`, powered by `metap-query::jql` — the generic query
 * language `ListInput.jql`/`?jql=` exposes on the same `/api/{entity}` list route every entity
 * already has (not a jira-specific endpoint). Any field name is validated against the entity's
 * own metadata server-side, so a typo or a disallowed operator comes back as a clean, readable
 * `invalid_jql` message rather than a generic failure — surfaced directly here since the message
 * is already meant for a human.
 */
export function AdvancedSearchPage() {
  const [draft, setDraft] = useState("");
  const [submitted, setSubmitted] = useState("");

  const {
    data: issues,
    error,
    isFetching,
  } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["advanced-search", submitted],
    `/api/jira.issues?jql=${encodeURIComponent(submitted)}&limit=100`,
    (response) => response.data,
  );

  function handleSubmit() {
    setSubmitted(draft.trim());
  }

  return (
    <Container size="lg" py="xl">
      <Title order={2} mb="md">
        Advanced Search
      </Title>

      <Card withBorder mb="md" padding="md">
        <Textarea
          label="Query"
          placeholder='e.g. priority = "high" AND status != "done" ORDER BY dueDate'
          value={draft}
          onChange={(event) => setDraft(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              handleSubmit();
            }
          }}
          autosize
          minRows={2}
          mb="xs"
        />
        <Group justify="space-between">
          <Text size="xs" c="dimmed">
            Examples:{" "}
            {EXAMPLES.map((ex) => (
              <Text
                key={ex}
                component="span"
                size="xs"
                c="blue"
                style={{ cursor: "pointer" }}
                mr="sm"
                onClick={() => setDraft(ex)}
              >
                {ex}
              </Text>
            ))}
          </Text>
          <Button size="sm" loading={isFetching} onClick={handleSubmit}>
            Search
          </Button>
        </Group>
      </Card>

      {error ? (
        <Alert color="red" title="Query error" mb="md">
          <ApiErrorMessage error={error} />
        </Alert>
      ) : null}

      {issues ? (
        <Card withBorder padding={0}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Title</Table.Th>
                <Table.Th>Type</Table.Th>
                <Table.Th>Priority</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th>Assignee</Table.Th>
                <Table.Th>Points</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {issues.map((issue) => (
                <Table.Tr key={issue.id}>
                  <Table.Td>
                    <Text component={Link} to={`/issues/${issue.id}`} size="sm" fw={600}>
                      {issue.data.title}
                    </Text>
                  </Table.Td>
                  <Table.Td>{issue.data.issueType ?? "—"}</Table.Td>
                  <Table.Td>
                    <Badge size="sm" color={PRIORITY_COLOR[issue.data.priority]} variant="light">
                      {issue.data.priority}
                    </Badge>
                  </Table.Td>
                  <Table.Td>{issue.status}</Table.Td>
                  <Table.Td>{issue.data.assigneeEmail ?? "Unassigned"}</Table.Td>
                  <Table.Td>{issue.data.storyPoints ?? "—"}</Table.Td>
                </Table.Tr>
              ))}
              {issues.length === 0 ? (
                <Table.Tr>
                  <Table.Td colSpan={6}>
                    <Text size="sm" c="dimmed" py="md" ta="center">
                      No issues match this query.
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ) : null}
            </Table.Tbody>
          </Table>
        </Card>
      ) : null}
    </Container>
  );
}
