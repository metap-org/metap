import { useQuery } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch } from "./client";

export function useApiQuery<TFetched, TSelected = TFetched>(
  queryKey: QueryKey,
  path: string,
  select?: (data: TFetched) => TSelected,
  enabled: boolean = true,
) {
  const { token } = useAuth();

  return useQuery({
    queryKey,
    queryFn: () => apiFetch<TFetched>(path, token),
    select,
    enabled: token !== null && enabled,
  });
}
