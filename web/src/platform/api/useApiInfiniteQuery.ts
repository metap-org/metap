import { useInfiniteQuery } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch } from "./client";

export function useApiInfiniteQuery<TFetched>(
  queryKey: QueryKey,
  buildPath: (cursor: string | null) => string,
  getNextCursor: (lastPage: TFetched) => string | null,
  enabled: boolean = true,
) {
  const { token } = useAuth();

  return useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) => apiFetch<TFetched>(buildPath(pageParam), token),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => getNextCursor(lastPage),
    enabled: token !== null && enabled,
  });
}
