import { useEffect, useMemo, useState } from "react";
import type { DragEvent } from "react";
import { Badge, Card, CardContent, Select, toast } from "@metap/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiFetch, ApiErrorMessage, useApiQuery, useAuth } from "@metap/platform-ui";
import type { IssueRecord, ListResponse, ProjectRecord, SprintRecord } from "../api/types";

const PRIORITY_BADGE: Record<string, "destructive" | "warning" | "default" | "secondary"> = {
  urgent: "destructive",
  high: "warning",
  medium: "default",
  low: "secondary",
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
    <Card draggable onDragStart={onDragStart} style={{ cursor: "grab" }}>
      <CardContent className="pt-3">
        <Link to={`/issues/${issue.id}`} className="text-sm font-semibold text-foreground">
          {issue.data.title}
        </Link>
        <div className="mt-1">
          <Badge variant={PRIORITY_BADGE[issue.data.priority] ?? "default"}>
            {issue.data.priority}
          </Badge>
        </div>
      </CardContent>
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
      toast(
        "Move failed — the issue may have changed since the board last loaded, reload and try again.",
        {
          variant: "destructive",
        },
      );
      await queryClient.invalidateQueries({ queryKey: issuesQueryKey });
    }
  }

  if (projectsLoading) return <div>Loading…</div>;

  return (
    <div className="mx-auto max-w-6xl py-8">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">Backlog</h2>
        <Select
          className="w-[280px]"
          placeholder="Select a project"
          options={(projects ?? []).map((p) => ({
            value: p.id,
            label: `${p.data.key} — ${p.data.name}`,
          }))}
          value={projectId ?? undefined}
          onValueChange={setProjectId}
        />
      </div>

      {issuesError ? <ApiErrorMessage error={issuesError} /> : null}
      {issuesLoading ? <div>Loading issues…</div> : null}

      {!issuesLoading && projectId ? (
        <div className="overflow-x-auto">
          <div
            className="grid gap-4"
            style={{
              gridTemplateColumns: `repeat(${Math.max(Math.min(columns.length, 4), 1)}, minmax(220px, 1fr))`,
            }}
          >
            {columns.map((column) => (
              <div
                key={column.id ?? "backlog"}
                className="flex min-h-[200px] flex-col gap-2 rounded-lg bg-muted p-2"
                onDragOver={(e) => e.preventDefault()}
                onDrop={(e) => void handleDrop(e, column.id)}
              >
                <p className="text-sm font-bold text-foreground">
                  {column.label} ({grouped.get(column.id)?.length ?? 0})
                </p>
                {(grouped.get(column.id) ?? []).map((issue) => (
                  <IssueCard
                    key={issue.id}
                    issue={issue}
                    onDragStart={(e) => handleDragStart(e, issue)}
                  />
                ))}
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}
