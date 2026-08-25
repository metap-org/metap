/**
 * A small, dependency-free bar chart — count/magnitude by category, the shape a dashboard's
 * "issues by status" / "records by X" widget needs. Generic on purpose: nothing here knows about
 * `jira.issues` or any other entity, it only takes `{label, value, color?}` rows — a future
 * customizable-dashboard widget or any other app in this monorepo can reuse it the same way
 * `RecordDetail`/`GeneratedList` already do for CRUD.
 *
 * Renders as inline SVG (no chart library dependency, same choice `apps/jira-fe`'s burndown
 * chart already made) and reads color/ink from Mantine CSS variables so it inherits the host
 * app's theme (including dark mode) for free rather than shipping a second, disconnected
 * palette. A single un-colored series draws every bar in one hue (`--mantine-color-blue-6`) —
 * per-bar `color` is for when category identity itself carries meaning beyond the axis label
 * (e.g. priority's red/orange/yellow/gray), not decoration.
 */

export type BarChartDatum = {
  label: string;
  value: number;
  /** A CSS color (e.g. `var(--mantine-color-red-6)` or a Mantine color name resolved by the
   *  caller) — omit to use the default single sequential hue for every bar. */
  color?: string;
};

export function BarChart({
  data,
  height = 200,
  ariaLabel,
}: {
  data: BarChartDatum[];
  height?: number;
  ariaLabel?: string;
}) {
  const padding = { top: 22, right: 8, bottom: 28, left: 8 };
  const barWidth = 44;
  const gap = 20;
  const innerW = Math.max(data.length * barWidth + Math.max(data.length - 1, 0) * gap, barWidth);
  const width = innerW + padding.left + padding.right;
  const innerH = height - padding.top - padding.bottom;
  const maxValue = Math.max(...data.map((d) => d.value), 1);
  const baselineY = height - padding.bottom;

  return (
    <svg
      width="100%"
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={ariaLabel ?? "Bar chart"}
      style={{ maxWidth: "100%", display: "block" }}
    >
      <line
        x1={padding.left}
        y1={baselineY}
        x2={width - padding.right}
        y2={baselineY}
        stroke="var(--mantine-color-gray-4)"
      />
      {data.map((d, i) => {
        const x = padding.left + i * (barWidth + gap);
        const barHeight =
          maxValue > 0 ? Math.max((d.value / maxValue) * innerH, d.value > 0 ? 2 : 0) : 0;
        const y = baselineY - barHeight;
        const color = d.color ?? "var(--mantine-color-blue-6)";
        return (
          <g key={d.label}>
            <title>{`${d.label}: ${d.value}`}</title>
            <rect x={x} y={y} width={barWidth} height={barHeight} rx={4} fill={color} />
            <text
              x={x + barWidth / 2}
              y={y - 6}
              textAnchor="middle"
              fontSize={12}
              fill="var(--mantine-color-dimmed)"
              style={{ fontVariantNumeric: "tabular-nums" }}
            >
              {d.value}
            </text>
            <text
              x={x + barWidth / 2}
              y={baselineY + 16}
              textAnchor="middle"
              fontSize={11}
              fill="var(--mantine-color-dimmed)"
            >
              {d.label}
            </text>
          </g>
        );
      })}
    </svg>
  );
}
