# jira-fe (scaffold)

Frontend for `apps/jira-server` (port 3100) — a demo/PoC app built to prove out `metap`'s
table-per-entity + workflow machinery with a real Jira-like UI (dashboard, kanban board, sprints,
comments), not a real product. See the root `CLAUDE.md`'s `apps/jira-server` bullet for backend
context, and `docs/roadmap.md`'s jira-server demo-buildout phase for what's built and what's next.

```bash
pnpm install
pnpm --filter @metap/jira-fe dev
```

Needs `apps/jira-server` running (`pnpm dev:jira:rs`) and a minted token for its tenant
(`pnpm mint:jira-token`, or the `LoginForm`'s real login once a user exists on that tenant).
