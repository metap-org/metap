import { LoginForm } from "@metap/platform-react";

/**
 * `LoginForm`'s `tenantId` prop (added when `metap-auth`'s tenant-auth phase shipped) routes
 * `POST /auth/login` through `Router::begin(tenantId)` instead of the global-by-email fallback —
 * required for this app's `DedicatedDb`-strategy tenant, whose `users` table lives only in its
 * own database (`apps/jira-server/.env`'s `JIRA_TENANT_ID`/`MY_JIRA_DSN`). Real login now reaches
 * it directly; the `PasteTokenFallback` this page used to also render (paste a
 * `pnpm mint:jira-token`-minted token by hand) is retired now that the actual gap it worked
 * around is closed.
 */
export function LoginPage() {
  return <LoginForm tenantId={import.meta.env.VITE_JIRA_TENANT_ID} />;
}
