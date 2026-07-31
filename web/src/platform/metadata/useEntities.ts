import { useApiQuery } from "../api/useApiQuery";
import type { EntitySummary } from "./types";

export function useEntities() {
  return useApiQuery<{ data: EntitySummary[] }, EntitySummary[]>(
    ["entities"],
    "/metadata/entities",
    (response) => response.data,
  );
}
