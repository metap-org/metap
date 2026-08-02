import { useMutation } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthContext";
import { apiFetch, type ApiError } from "./client";

export function useApiMutation<TResponse, TBody = unknown>(method: "POST" | "PATCH", path: string) {
  const { token } = useAuth();

  return useMutation<TResponse, ApiError, TBody>({
    mutationFn: (body: TBody) =>
      apiFetch<TResponse>(path, token, {
        method,
        body: JSON.stringify(body),
      }),
  });
}
