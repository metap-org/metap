import type { z } from "zod";

export type FieldKind =
  | "id"
  | "string"
  | "number"
  | "boolean"
  | "date"
  | "datetime"
  | "money"
  | "enum"
  | "reference"
  | "json";

export type EntityField = {
  name: string;
  label: string;
  kind: FieldKind;
  required?: boolean;
  indexed?: boolean;
  unique?: boolean;
  enumValues?: readonly string[];
  refEntity?: string;
  searchable?: boolean;
  sortable?: boolean;
};

export type EntityListView = {
  name: string;
  label: string;
  fields: readonly string[];
  filters: readonly string[];
  defaultSort?: string;
  maxLimit: number;
};

export type WorkflowTransition = {
  action: string;
  from: string;
  to: string;
  label: string;
};

export type EntityWorkflow = {
  stateField: string;
  initialState: string;
  terminalStates: readonly string[];
  transitions: readonly WorkflowTransition[];
};

export type EntityPermissions = {
  read?: readonly string[];
  create?: readonly string[];
  update?: readonly string[];
};

export type EntityDefinition<TInput extends z.ZodTypeAny = z.ZodTypeAny> = {
  name: string;
  label: string;
  tableName: string;
  schema: TInput;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: EntityWorkflow;
  permissions?: EntityPermissions;
};
