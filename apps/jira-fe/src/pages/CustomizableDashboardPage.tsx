import { useEffect, useMemo, useState } from "react";
import GridLayout, { WidthProvider } from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";
import {
  ActionIcon,
  Button,
  Card,
  Container,
  Group,
  SegmentedControl,
  Select,
  Text,
  TextInput,
  Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { apiFetch, BarChart, useApiQuery, useAuth, useHasRole } from "@metap/platform-react";
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
      <Text fw={600} size="sm" mb="xs">
        {config.title}
      </Text>
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
      <Text fw={600} size="sm" mb="xs">
        {config.title}
      </Text>
      {error ? (
        <Text size="xs" c="red">
          Query error
        </Text>
      ) : (
        <Text size="2rem" fw={700}>
          {issues?.length ?? "…"}
        </Text>
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
 * `react-grid-layout` (added as a `jira-fe`-only dependency, not pushed into `platform-react` —
 * unlike `BarChart`, forcing every future app onto a grid-layout library isn't justified by one
 * app's ask) while each widget's *rendering* reuses the generic `BarChart` from
 * `packages/platform-react`.
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
      notifications.show({ color: "green", message: "Dashboard saved." });
    } catch {
      notifications.show({ color: "red", message: "Failed to save dashboard." });
    } finally {
      setSaving(false);
    }
  }

  const [statJql, setStatJql] = useState('status != "done"');

  return (
    <Container size="xl" py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>Dashboard</Title>
        <Group>
          {isAdmin ? (
            <SegmentedControl
              value={scope}
              onChange={(value) => setScope(value as "personal" | "tenant")}
              data={[
                { label: "My dashboard", value: "personal" },
                { label: "Organization default", value: "tenant" },
              ]}
            />
          ) : null}
          <Button variant={editing ? "filled" : "outline"} onClick={() => setEditing((e) => !e)}>
            {editing ? "Done editing" : "Edit"}
          </Button>
          {editing && dirty ? (
            <Button onClick={() => void handleSave()} loading={saving}>
              Save
            </Button>
          ) : null}
        </Group>
      </Group>

      {editing ? (
        <Card withBorder mb="md" padding="md">
          <Text fw={600} mb="xs">
            Add a widget
          </Text>
          <Group align="flex-end">
            <Select
              label="Bar chart"
              placeholder="Group by…"
              data={[
                { value: "status", label: "By status" },
                { value: "priority", label: "By priority" },
                { value: "issueType", label: "By issue type" },
              ]}
              onChange={(value) => {
                if (!value) return;
                const groupBy = value as BarChartWidgetConfig["groupBy"];
                addWidget({
                  type: "barChart",
                  title: `Issues ${value === "status" ? "by status" : `by ${value}`}`,
                  groupBy,
                });
              }}
            />
            <TextInput
              label="Stat tile query"
              w={280}
              value={statJql}
              onChange={(event) => setStatJql(event.currentTarget.value)}
            />
            <Button
              onClick={() => addWidget({ type: "statTile", title: statJql, jql: statJql })}
              disabled={!statJql.trim()}
            >
              Add stat tile
            </Button>
          </Group>
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
              <Card withBorder padding="sm" h="100%" style={{ overflow: "hidden" }}>
                {editing ? (
                  <ActionIcon
                    size="sm"
                    variant="subtle"
                    color="red"
                    style={{ position: "absolute", top: 6, right: 6, zIndex: 1 }}
                    onClick={() => removeWidget(widget.id)}
                  >
                    ×
                  </ActionIcon>
                ) : null}
                <WidgetView widget={widget} />
              </Card>
            </div>
          ))}
        </ResponsiveGridLayout>
      ) : widgets ? (
        <Text c="dimmed">No widgets yet — click Edit to add one.</Text>
      ) : null}
    </Container>
  );
}
