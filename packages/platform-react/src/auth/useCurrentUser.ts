import { useApiQuery } from "../api/useApiQuery";

export type CurrentUser = {
  userId: string | null;
  tenantId: string;
  roles: string[];
};

/** Backed by `GET /auth/me` — the frontend's only way to know the caller's roles, since roles
 * are deliberately never encoded on the JWT itself (looked up fresh per request server-side). */
export function useCurrentUser() {
  return useApiQuery<{ data: CurrentUser }, CurrentUser>(
    ["currentUser"],
    "/auth/me",
    (response) => response.data,
  );
}
