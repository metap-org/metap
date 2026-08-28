import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Card, CardContent, Input, Table, TableBody, TableCell, TableRow } from "@metap/ui";
import { ApiErrorMessage, useApiQuery } from "@metap/platform-ui";
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
    <div className="mx-auto max-w-4xl py-8">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">Logwork Report</h2>
        <div className="flex items-center gap-2">
          <Input
            label="From"
            type="date"
            value={from}
            onChange={(e) => setFrom(e.currentTarget.value)}
          />
          <Input label="To" type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        </div>
      </div>

      {error ? (
        <Card className="mb-4">
          <CardContent className="pt-4">
            <ApiErrorMessage error={error} />
          </CardContent>
        </Card>
      ) : null}

      {worklogs ? (
        <>
          <p className="mb-4 text-foreground">
            Total logged: <strong>{formatMinutes(grandTotal)}</strong> across {worklogs.length} entr
            {worklogs.length === 1 ? "y" : "ies"}
            {isFetching ? " (refreshing…)" : ""}
          </p>

          <div className="flex flex-col gap-4">
            {byAuthor.map(({ author, entries, total }) => (
              <Card key={author}>
                <CardContent className="pt-4">
                  <div className="mb-2 flex items-center justify-between">
                    <p className="font-semibold text-foreground">{author}</p>
                    <p className="font-semibold text-foreground">{formatMinutes(total)}</p>
                  </div>
                  <Table>
                    <TableBody>
                      {entries.map((w) => (
                        <TableRow key={w.id}>
                          <TableCell className="w-[120px]">{w.data.workDate}</TableCell>
                          <TableCell>
                            <Link
                              to={`/issues/${w.data.issue}`}
                              className="text-sm text-foreground"
                            >
                              {w.relatedDisplay?.issue ?? w.data.issue}
                            </Link>
                          </TableCell>
                          <TableCell className="w-[100px]">
                            {formatMinutes(w.data.timeSpentMinutes)}
                          </TableCell>
                          <TableCell className="text-muted-foreground">
                            {w.data.description}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            ))}
            {byAuthor.length === 0 ? (
              <p className="text-muted-foreground">No work logged in this date range.</p>
            ) : null}
          </div>
        </>
      ) : null}
    </div>
  );
}
