import { useEffect, useMemo, useState } from "react";
import {
  Badge,
  Card,
  CardContent,
  Table,
  TableBody,
  TableCell,
  TableRow,
  TableHead,
  TableHeader,
  Input,
} from "@metap/ui";
import { Link } from "react-router-dom";
import { ApiErrorMessage, BarChart, useApiQuery } from "@metap/platform-ui";
import type { BarChartDatum } from "@metap/platform-ui";
import type { IssueRecord, ListResponse } from "../api/types";

/** `@metap/ui` has no `useDebouncedValue` equivalent (a component library, not a hooks
 *  package) — hand-written, same shape `platform-ui`'s `ReferenceFieldInput`/`GeneratedList`
 *  already duplicate locally rather than sharing a file. */
function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}

/**
 * `title` is `searchable` on `jira.issues` (`issue_entity.rs`) — substring/ILIKE match, so
 * `?title=<term>` already works through the generic `QueryPlanner`. No dedicated search page:
 * this box lives on the page everyone already lands on first.
 */
function SearchBox() {
  const [term, setTerm] = useState("");
  const debounced = useDebouncedValue(term, 300);

  const { data: results } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["search-issues", debounced],
    `/api/jira.issues?title=${encodeURIComponent(debounced)}&limit=10`,
    (response) => response.data,
    debounced.trim().length > 0,
  );

  return (
    <Card className="mb-8">
      <CardContent className="pt-4">
        <Input
          placeholder="Search issues by title…"
          value={term}
          onChange={(event) => setTerm(event.currentTarget.value)}
        />
        {debounced.trim().length > 0 ? (
          <Table className="mt-2">
            <TableBody>
              {(results ?? []).map((issue) => (
                <TableRow key={issue.id}>
                  <TableCell>
                    <Link to={`/issues/${issue.id}`}>{issue.data.title}</Link>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{issue.status}</Badge>
                  </TableCell>
                </TableRow>
              ))}
              {results?.length === 0 ? (
                <TableRow>
                  <TableCell>
                    <span className="text-sm text-muted-foreground">No matching issues.</span>
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        ) : null}
      </CardContent>
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

const PRIORITY_BADGE: Record<string, "destructive" | "warning" | "default" | "secondary"> = {
  urgent: "destructive",
  high: "warning",
  medium: "default",
  low: "secondary",
};

const PRIORITY_CHART_COLOR: Record<string, string> = {
  urgent: "hsl(var(--destructive))",
  high: "#f59e0b",
  medium: "#eab308",
  low: "hsl(var(--muted-foreground))",
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
    color: PRIORITY_CHART_COLOR[key],
  }));

  if (isLoading) return <div>Loading…</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <div className="mx-auto max-w-5xl py-8">
      <h2 className="mb-4 text-xl font-semibold text-foreground">Dashboard</h2>

      <SearchBox />

      <div className="mb-8 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Card>
          <CardContent className="pt-4">
            <p className="mb-2 font-semibold text-foreground">By status</p>
            <BarChart data={statusChartData} ariaLabel="Issues by status" />
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4">
            <p className="mb-2 font-semibold text-foreground">By priority</p>
            <BarChart data={priorityChartData} ariaLabel="Issues by priority" />
          </CardContent>
        </Card>
      </div>

      <p className="mb-2 font-semibold text-foreground">Recently created</p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Title</TableHead>
            <TableHead>Priority</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Assignee</TableHead>
            <TableHead>Due</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {recent.map((issue) => (
            <TableRow key={issue.id}>
              <TableCell>
                <Link to={`/issues/${issue.id}`}>{issue.data.title}</Link>
              </TableCell>
              <TableCell>
                <Badge variant={PRIORITY_BADGE[issue.data.priority] ?? "default"}>
                  {issue.data.priority}
                </Badge>
              </TableCell>
              <TableCell>{STATUS_LABEL[issue.status] ?? issue.status}</TableCell>
              <TableCell>{issue.data.assigneeEmail ?? "—"}</TableCell>
              <TableCell>{issue.data.dueDate ?? "—"}</TableCell>
            </TableRow>
          ))}
          {recent.length === 0 ? (
            <TableRow>
              <TableCell colSpan={5}>
                <span className="text-muted-foreground">No issues yet.</span>
              </TableCell>
            </TableRow>
          ) : null}
        </TableBody>
      </Table>
    </div>
  );
}
