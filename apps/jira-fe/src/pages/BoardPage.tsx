import { useEffect, useMemo, useState } from "react";
import type { DragEvent } from "react";
import { Badge, Card, CardContent, Select, toast } from "@metap/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiFetch, ApiErrorMessage, useApiQuery, useAuth, useEntity } from "@metap/platform-ui";
import type { IssueRecord, ListResponse, ProjectRecord } from "../api/types";

const FALLBACK_STATUS_ORDER = ["todo", "in_progress", "in_review", "done"];

const STATUS_LABEL: Record<string, string> = {
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
};

const PRIORITY_BADGE: Record<string, "destructive" | "warning" | "default" | "secondary"> = {
  urgent: "destructive",
  high: "warning",
  medium: "default",
  low: "secondary",
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
    <Card draggable onDragStart={onDragStart} style={{ cursor: "grab" }}>
      <CardContent className="pt-3">
        <Link to={`/issues/${issue.id}`} className="text-sm font-semibold text-foreground">
          {issue.data.title}
        </Link>
        <div className="mt-1 flex items-center gap-1">
          <Badge variant={PRIORITY_BADGE[issue.data.priority] ?? "default"}>
            {issue.data.priority}
          </Badge>
          {issue.data.dueDate ? <Badge variant="outline">{issue.data.dueDate}</Badge> : null}
        </div>
        {issue.data.assigneeEmail ? (
          <p className="mt-1 text-xs text-muted-foreground">{issue.data.assigneeEmail}</p>
        ) : null}
      </CardContent>
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
      toast(
        `No such transition: an issue can't move directly from "${STATUS_LABEL[payload.status] ?? payload.status}" to "${STATUS_LABEL[targetStatus] ?? targetStatus}".`,
        { variant: "destructive" },
      );
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
      toast(
        "Transition failed — the issue may have changed since the board last loaded, reload and try again.",
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
        <h2 className="text-xl font-semibold text-foreground">Board</h2>
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
              gridTemplateColumns: `repeat(${Math.max(statusColumns.length, 1)}, minmax(220px, 1fr))`,
            }}
          >
            {statusColumns.map((status) => (
              <div
                key={status}
                className="flex min-h-[200px] flex-col gap-2 rounded-lg bg-muted p-2"
                onDragOver={(e) => e.preventDefault()}
                onDrop={(e) => void handleDrop(e, status)}
              >
                <p className="text-sm font-bold text-foreground">
                  {STATUS_LABEL[status] ?? status} ({columns.get(status)?.length ?? 0})
                </p>
                {(columns.get(status) ?? []).map((issue) => (
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
