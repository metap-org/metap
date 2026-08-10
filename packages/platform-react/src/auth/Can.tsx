import type { ReactNode } from "react";
import { useCurrentUser } from "./useCurrentUser";

/** True once `useCurrentUser()` resolves and the caller holds any of `role`. UI-only gating —
 * the server re-checks via `AdminContext` regardless, so a false negative here is just a
 * hidden button, never a security boundary. */
export function useHasRole(role: string | string[]): boolean {
  const { data: user } = useCurrentUser();
  const required = Array.isArray(role) ? role : [role];

  if (!user) {
    return false;
  }

  return required.some((r) => user.roles.includes(r));
}

/** Renders `children` only if the current user holds any of `roles`, `fallback` otherwise
 * (nothing by default). Hides while `useCurrentUser()` is still loading, to avoid a flash of
 * gated content before roles resolve. */
export function Can({
  roles,
  children,
  fallback = null,
}: {
  roles: string | string[];
  children: ReactNode;
  fallback?: ReactNode;
}) {
  const allowed = useHasRole(roles);
  return <>{allowed ? children : fallback}</>;
}
