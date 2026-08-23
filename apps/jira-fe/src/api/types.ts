// Local, hand-written types for this demo app's own pages (Dashboard/Board) — deliberately not
// the generated `EntitySummary`/`EntityField` types from `@metap/platform-react` (those describe
// *metadata*, not a *record*'s wire shape). `RecordDto`'s wire shape isn't exported from
// `@metap/platform-react` either (`WorkflowActionBar` defines its own minimal local copy) — same
// approach here, just wider (this app's pages read more fields off an issue than the action bar
// does).

export type ListResponse<T> = {
  data: T[];
  page: { limit: number; nextCursor: string | null };
};

export type IssuePriority = "low" | "medium" | "high" | "urgent";

export type IssueData = {
  title: string;
  description?: string;
  priority: IssuePriority;
  project: string;
  sprint?: string;
  assigneeEmail?: string;
  reporterEmail: string;
  dueDate?: string;
};

export type IssueRecord = {
  id: string;
  version: number;
  status: string;
  createdAt: string;
  relatedDisplay?: Record<string, string>;
  data: IssueData;
};

export type ProjectData = {
  key: string;
  name: string;
  description?: string;
};

export type ProjectRecord = {
  id: string;
  data: ProjectData;
};
