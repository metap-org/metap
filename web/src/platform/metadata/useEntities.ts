import { useApiQuery } from "../api/useApiQuery";

export type EntityField = {
  name: string;
  label: string;
  kind: string;
  required?: boolean;
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

export type EntitySummary = {
  name: string;
  label: string;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: unknown;
};

export function useEntities() {
  return useApiQuery<{ data: EntitySummary[] }, EntitySummary[]>(
    ["entities"],
    "/metadata/entities",
    (response) => response.data,
  );
}
