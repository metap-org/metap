# 11. Risks and Technical Debt

Honest, grounded list — not padded with hypotheticals. Each item names its actual trigger for being addressed, per this project's trigger-based evolution stance ([04. Solution Strategy](04-strategy.md)).

| Risk / Debt | Impact | Trigger to address |
|---|---|---|
| Single generic `records` table for every entity | A very high-volume or accounting-critical entity shares indexes/row layout with everything else; JSONB access is slower than a typed column for heavy write/aggregate workloads | A concrete entity's measured performance need — see Data Model Strategy Step 3, [05. Building Block View](05-building-blocks.md) |
| No report/analytics query path | A future dashboard or export feature would have to run directly against the OLTP `records` table, competing with live traffic | A real reporting UI/consumer shows up, or an OLTP-path query is measurably slowed by report-shaped access — see `docs/roadmap.md` Phase 4 |
| Phase 8 (Hardening) not started | No documented production security/ops posture: no confirmed non-root container setup, no secrets-management audit, no tuned rate limits beyond the default 300/min | Approaching a real production deployment |
| Frontend Core only Partial | `GeneratedForm`, `WorkflowActionBar`, permission-aware UI state, and table virtualization aren't built yet (`FieldRenderer`'s read-only half, `FieldValue`, is done — `FieldInput` is not) — this repo's own convention of browser-verifying UI changes can't fully apply yet | Continuing Phase 6 |
| No production deployment topology documented | No load balancer, no orchestrator config, no autoscaling, no secrets manager — see [07. Deployment View](07-deployment.md) | Same as Hardening trigger above |
| Single PostgreSQL instance, single RabbitMQ instance | No HA/replica story; a DB outage stops both reads and writes on the API (the outbox worker degrades gracefully by design, the API does not) | Real uptime requirements being defined (part of Hardening) |
| `IndexReconciler` has no build-concurrency governor | A metadata change adding several new `indexed`/`unique`/`searchMode` fields at once could trigger several concurrent `CREATE INDEX CONCURRENTLY` builds against the same DB at boot | Observed contention in practice — no entity has needed more than 2-3 indexed fields so far |
| Multi-service split not built | `crm.customers` is still the only entity/module; splitting into `packages/core` + `apps/<module>` is designed for but not built | A second, genuinely separate module needs to exist as its own deployable unit — see [03. System Scope and Context](03-context.md) and `docs/superpowers/specs/2026-07-29-multi-service-target-architecture-design.md` |
| Frontend platform package not published | `web/src/platform/` isn't yet an installable package — a second downstream project (possibly a monorepo, possibly a micro-frontend) would have to copy code, not `npm install` it | A second real consumer of `web/src/platform/` needs to exist — see [04. Solution Strategy](04-strategy.md)'s "Future Evolution: Frontend Platform Package" |

## Already-mitigated risks (kept for institutional memory)

- **Record-level read policy could empty an admin's `list()` results.** Found during Phase 3 manual E2E verification, fixed 2026-08-01 with a regression test — see [09. Architecture Decisions](09-adr.md).
- **Field masking didn't cover top-level `code`/`status` mirror columns.** Same fix pass as above.
- **An `IndexReconciler`-built index was silently never used** because its expression (`data->>'field'`) didn't syntactically match `QueryPlanner`'s query expression (`jsonb_extract_path_text`). Found and fixed during the Hot Field Index Strategy sub-project, before it was ever committed — see [09. Architecture Decisions](09-adr.md).
