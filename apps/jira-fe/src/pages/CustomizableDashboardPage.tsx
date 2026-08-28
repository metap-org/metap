import { useEffect, useMemo, useState } from "react";
import GridLayout, { WidthProvider } from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";
import {
  Button,
  Card,
  CardContent,
  IconButton,
  Input,
  Select,
  Spinner,
  Tabs,
  TabsList,
  TabsTrigger,
  toast,
} from "@metap/ui";
import { apiFetch, BarChart, useApiQuery, useAuth, useHasRole } from "@metap/platform-ui";
import type { IssueRecord, ListResponse } from "../api/types";

const ResponsiveGridLayout = WidthProvider(GridLayout);

type BarChartWidgetConfig = {
  type: "barChart";
  title: string;
  groupBy: "status" | "priority" | "issueType";
};
type StatTileWidgetConfig = { type: "statTile"; title: string; jql: string };
type WidgetConfig = BarChartWidgetConfig | StatTileWidgetConfig;
type DashboardWidget = { id: string; x: number; y: number; w: number; h: number } & WidgetConfig;
type DashboardLayout = { widgets: DashboardWidget[] };

type DashboardConfigDto = {
  id: string;
  ownerUserId: string | null;
  layout: DashboardLayout;
  updatedAt: string;
};

function newWidgetId() {
  return `w${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

function BarChartWidgetView({ config }: { config: BarChartWidgetConfig }) {
  const { data: issues } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["dashboard-widget-issues"],
    "/api/jira.issues?limit=200",
    (response) => response.data,
  );

  const counts = useMemo(() => {
    const map = new Map<string, number>();
    for (const issue of issues ?? []) {
      const key = config.groupBy === "status" ? issue.status : (issue.data[config.groupBy] ?? "—");
      map.set(key, (map.get(key) ?? 0) + 1);
    }
    return [...map.entries()].map(([label, value]) => ({ label, value }));
  }, [issues, config.groupBy]);

  return (
    <>
      <p className="mb-1 text-sm font-semibold text-foreground">{config.title}</p>
      <BarChart data={counts} height={140} ariaLabel={config.title} />
    </>
  );
}

function StatTileWidgetView({ config }: { config: StatTileWidgetConfig }) {
  const { data: issues, error } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["dashboard-widget-stat", config.jql],
    `/api/jira.issues?jql=${encodeURIComponent(config.jql)}&limit=200`,
    (response) => response.data,
  );

  return (
    <>
      <p className="mb-1 text-sm font-semibold text-foreground">{config.title}</p>
      {error ? (
        <p className="text-xs text-destructive">Query error</p>
      ) : (
        <p className="text-3xl font-bold text-foreground">{issues?.length ?? "…"}</p>
      )}
    </>
  );
}

function WidgetView({ widget }: { widget: DashboardWidget }) {
  if (widget.type === "barChart") return <BarChartWidgetView config={widget} />;
  return <StatTileWidgetView config={widget} />;
}

/**
 * Per-user or tenant-default dashboard layout, backed by `metap-dashboards`
 * (`GET/PUT /dashboards/me`, `GET/PUT /dashboards/tenant-default` — `crates/metap-http/src/routes/dashboards.rs`).
 * Widget catalog is deliberately small for v1 (bar chart by field, stat tile from a JQL query) —
 * `recentList` and others are natural additions later, not built here. Grid/drag/resize uses
 * `react-grid-layout` (added as a `jira-fe`-only dependency, not pushed into `platform-ui` —
 * unlike `BarChart`, forcing every future app onto a grid-layout library isn't justified by one
 * app's ask) while each widget's *rendering* reuses the generic `BarChart` from
 * `@metap/platform-ui`.
 */
export function CustomizableDashboardPage() {
  const { token } = useAuth();
  const isAdmin = useHasRole("admin");
  const [scope, setScope] = useState<"personal" | "tenant">("personal");
  const [widgets, setWidgets] = useState<DashboardWidget[] | null>(null);
  const [dirty, setDirty] = useState(false);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  const endpoint = scope === "tenant" ? "/dashboards/tenant-default" : "/dashboards/me";
  const { data: remote } = useApiQuery<
    { data: DashboardConfigDto | null },
    DashboardConfigDto | null
  >(["dashboard-config", scope], endpoint, (response) => response.data);

  useEffect(() => {
    setWidgets(remote?.layout.widgets ?? []);
    setDirty(false);
  }, [remote]);

  function addWidget(config: WidgetConfig) {
    setWidgets((prev) => [
      ...(prev ?? []),
      { id: newWidgetId(), x: 0, y: Infinity, w: 4, h: 3, ...config },
    ]);
    setDirty(true);
  }

  function removeWidget(id: string) {
    setWidgets((prev) => (prev ?? []).filter((w) => w.id !== id));
    setDirty(true);
  }

  function handleLayoutChange(layout: { i: string; x: number; y: number; w: number; h: number }[]) {
    setWidgets((prev) =>
      (prev ?? []).map((w) => {
        const l = layout.find((item) => item.i === w.id);
        return l ? { ...w, x: l.x, y: l.y, w: l.w, h: l.h } : w;
      }),
    );
    setDirty(true);
  }

  async function handleSave() {
    if (!widgets) return;
    setSaving(true);
    try {
      await apiFetch(endpoint, token, {
        method: "PUT",
        body: JSON.stringify({ layout: { widgets } satisfies DashboardLayout }),
      });
      setDirty(false);
      toast("Dashboard saved.");
    } catch {
      toast("Failed to save dashboard.", { variant: "destructive" });
    } finally {
      setSaving(false);
    }
  }

  const [statJql, setStatJql] = useState('status != "done"');
  const [barChartGroupBy, setBarChartGroupBy] = useState<string | undefined>(undefined);

  return (
    <div className="mx-auto max-w-6xl py-8">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">Dashboard</h2>
        <div className="flex items-center gap-2">
          {isAdmin ? (
            <Tabs value={scope} onValueChange={(value) => setScope(value as "personal" | "tenant")}>
              <TabsList>
                <TabsTrigger value="personal">My dashboard</TabsTrigger>
                <TabsTrigger value="tenant">Organization default</TabsTrigger>
              </TabsList>
            </Tabs>
          ) : null}
          <Button variant={editing ? "default" : "outline"} onClick={() => setEditing((e) => !e)}>
            {editing ? "Done editing" : "Edit"}
          </Button>
          {editing && dirty ? (
            <Button onClick={() => void handleSave()} disabled={saving}>
              {saving ? <Spinner size="sm" className="mr-2" /> : null}
              Save
            </Button>
          ) : null}
        </div>
      </div>

      {editing ? (
        <Card className="mb-4">
          <CardContent className="pt-4">
            <p className="mb-2 font-semibold text-foreground">Add a widget</p>
            <div className="flex items-end gap-2">
              <Select
                label="Bar chart"
                placeholder="Group by…"
                options={[
                  { value: "status", label: "By status" },
                  { value: "priority", label: "By priority" },
                  { value: "issueType", label: "By issue type" },
                ]}
                value={barChartGroupBy}
                onValueChange={(value) => {
                  const groupBy = value as BarChartWidgetConfig["groupBy"];
                  addWidget({
                    type: "barChart",
                    title: `Issues ${value === "status" ? "by status" : `by ${value}`}`,
                    groupBy,
                  });
                  setBarChartGroupBy(undefined);
                }}
              />
              <Input
                label="Stat tile query"
                className="w-[280px]"
                value={statJql}
                onChange={(event) => setStatJql(event.currentTarget.value)}
              />
              <Button
                onClick={() => addWidget({ type: "statTile", title: statJql, jql: statJql })}
                disabled={!statJql.trim()}
              >
                Add stat tile
              </Button>
            </div>
          </CardContent>
        </Card>
      ) : null}

      {widgets && widgets.length > 0 ? (
        <ResponsiveGridLayout
          cols={12}
          rowHeight={40}
          isDraggable={editing}
          isResizable={editing}
          onLayoutChange={handleLayoutChange}
        >
          {widgets.map((widget) => (
            <div key={widget.id} data-grid={{ x: widget.x, y: widget.y, w: widget.w, h: widget.h }}>
              <Card className="relative h-full overflow-hidden">
                <CardContent className="pt-3">
                  {editing ? (
                    <IconButton
                      size="sm"
                      variant="ghost"
                      aria-label="Remove widget"
                      className="absolute right-1.5 top-1.5 z-10 text-destructive hover:text-destructive"
                      onClick={() => removeWidget(widget.id)}
                      icon={<span className="text-base leading-none">×</span>}
                    />
                  ) : null}
                  <WidgetView widget={widget} />
                </CardContent>
              </Card>
            </div>
          ))}
        </ResponsiveGridLayout>
      ) : widgets ? (
        <p className="text-muted-foreground">No widgets yet — click Edit to add one.</p>
      ) : null}
    </div>
  );
}
