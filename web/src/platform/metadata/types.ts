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
  searchable?: boolean;
  sortable?: boolean;
  enumValues?: readonly string[];
  refEntity?: string;
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

export type EntitySummary = {
  name: string;
  label: string;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: EntityWorkflow;
  version?: string;
};
