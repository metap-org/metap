import { z } from "zod";

// Describes exactly what crosses the wire in a GET /metadata/entities(/:name)
// response — not backend's internal EntityField/EntityWorkflow TS types.
// This is the source of truth the frontend's generated types are derived
// from (via the OpenAPI doc + openapi-typescript), so it deliberately
// excludes anything that never actually reaches the client — most notably
// WorkflowTransition.guard, a server-side function stripped by
// JSON.stringify before a response is ever sent.

export const FieldKindSchema = z.enum([
  "id",
  "string",
  "number",
  "boolean",
  "date",
  "datetime",
  "money",
  "enum",
  "reference",
  "json",
]);

export const EntityFieldSchema = z.object({
  name: z.string(),
  label: z.string(),
  kind: FieldKindSchema,
  required: z.boolean().optional(),
  indexed: z.boolean().optional(),
  unique: z.boolean().optional(),
  enumValues: z.array(z.string()).optional(),
  refEntity: z.string().optional(),
  searchable: z.boolean().optional(),
  searchMode: z.enum(["substring", "fts"]).optional(),
  sortable: z.boolean().optional(),
});

export const EntityListViewSchema = z.object({
  name: z.string(),
  label: z.string(),
  fields: z.array(z.string()),
  filters: z.array(z.string()),
  defaultSort: z.string().optional(),
  maxLimit: z.number(),
});

export const WorkflowTransitionSchema = z.object({
  action: z.string(),
  from: z.string(),
  to: z.string(),
  label: z.string(),
});

export const EntityWorkflowSchema = z.object({
  stateField: z.string(),
  initialState: z.string(),
  terminalStates: z.array(z.string()),
  transitions: z.array(WorkflowTransitionSchema),
});

export const EntitySummarySchema = z.object({
  name: z.string(),
  label: z.string(),
  fields: z.array(EntityFieldSchema),
  listViews: z.array(EntityListViewSchema),
  workflow: EntityWorkflowSchema.optional(),
  version: z.string(),
});
