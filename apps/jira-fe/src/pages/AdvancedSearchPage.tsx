import { useState } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  AlertTitle,
  AlertDescription,
  Badge,
  Button,
  Card,
  CardContent,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  Textarea,
  Spinner,
} from "@metap/ui";
import { ApiErrorMessage, useApiQuery } from "@metap/platform-ui";
import type { IssueRecord, ListResponse } from "../api/types";

const PRIORITY_BADGE: Record<string, "destructive" | "warning" | "default" | "secondary"> = {
  urgent: "destructive",
  high: "warning",
  medium: "default",
  low: "secondary",
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
    <div className="mx-auto max-w-4xl py-8">
      <h2 className="mb-4 text-xl font-semibold text-foreground">Advanced Search</h2>

      <Card className="mb-4">
        <CardContent className="pt-4">
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
            rows={2}
            className="mb-2"
          />
          <div className="flex items-center justify-between">
            <p className="text-xs text-muted-foreground">
              Examples:{" "}
              {EXAMPLES.map((ex) => (
                <button
                  key={ex}
                  type="button"
                  onClick={() => setDraft(ex)}
                  className="mr-2 cursor-pointer text-xs text-primary underline-offset-2 hover:underline"
                >
                  {ex}
                </button>
              ))}
            </p>
            <Button size="sm" disabled={isFetching} onClick={handleSubmit}>
              {isFetching ? <Spinner size="sm" className="mr-2" /> : null}
              Search
            </Button>
          </div>
        </CardContent>
      </Card>

      {error ? (
        <Alert variant="destructive" className="mb-4">
          <AlertTitle>Query error</AlertTitle>
          <AlertDescription>
            <ApiErrorMessage error={error} />
          </AlertDescription>
        </Alert>
      ) : null}

      {issues ? (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Title</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Priority</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Assignee</TableHead>
                <TableHead>Points</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {issues.map((issue) => (
                <TableRow key={issue.id}>
                  <TableCell>
                    <Link
                      to={`/issues/${issue.id}`}
                      className="text-sm font-semibold text-foreground"
                    >
                      {issue.data.title}
                    </Link>
                  </TableCell>
                  <TableCell>{issue.data.issueType ?? "—"}</TableCell>
                  <TableCell>
                    <Badge variant={PRIORITY_BADGE[issue.data.priority] ?? "default"}>
                      {issue.data.priority}
                    </Badge>
                  </TableCell>
                  <TableCell>{issue.status}</TableCell>
                  <TableCell>{issue.data.assigneeEmail ?? "Unassigned"}</TableCell>
                  <TableCell>{issue.data.storyPoints ?? "—"}</TableCell>
                </TableRow>
              ))}
              {issues.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6}>
                    <p className="py-4 text-center text-sm text-muted-foreground">
                      No issues match this query.
                    </p>
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </Card>
      ) : null}
    </div>
  );
}
