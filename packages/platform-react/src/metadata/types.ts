// Thin façade over the generated wire-contract types — do not hand-add
// fields here. Run `pnpm generate:types` (dev server must be running) after
// a backend meta-model change, which regenerates ./generated-types.ts; these
// exports pick the change up automatically since they're derived by
// indexing into it, not redeclared.
import type { components } from "./generated-types";

export type EntitySummary = components["schemas"]["EntitySummary"];
export type EntityField = EntitySummary["fields"][number];
export type EntityListView = EntitySummary["listViews"][number];
export type EntityWorkflow = NonNullable<EntitySummary["workflow"]>;
export type WorkflowTransition = EntityWorkflow["transitions"][number];
export type FieldKind = EntityField["kind"];
