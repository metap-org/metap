import { useApiQuery } from "../api/useApiQuery";
import type { EntitySummary } from "./types";

export function useEntity(entityName: string, enabled: boolean = true) {
  return useApiQuery<{ data: EntitySummary }, EntitySummary>(
    ["entity", entityName],
    `/metadata/entities/${entityName}`,
    (response) => response.data,
    enabled && entityName.length > 0,
  );
}
