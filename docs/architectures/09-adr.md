# 9. Architecture Decisions

This project already runs an informal ADR workflow: every non-trivial change gets a design spec (Motivation → Design → Out of scope) written and approved *before* an implementation plan or code, under `docs/superpowers/specs/`. Rather than duplicating that content here, this is a decision log indexing it.

| Date | Decision | Spec |
|---|---|---|
| 2026-07-28 | Auth + RequestContext + structured errors kernel | `docs/superpowers/specs/2026-07-28-auth-context-kernel-design.md` |
| 2026-07-28 | `CrudService.update` + optimistic locking | `docs/superpowers/specs/2026-07-28-crud-update-optimistic-locking-design.md` |
| 2026-07-29 | Target architecture: Metap as core platform for a multi-service ERP | `docs/superpowers/specs/2026-07-29-multi-service-target-architecture-design.md` |
| 2026-07-29 | `PermissionService`: entity-level RBAC (Phase 0 scaffold) | `docs/superpowers/specs/2026-07-29-permission-service-rbac-design.md` |
| 2026-07-29 | `QueryPlanner` hardening: metadata-constrained filters + real sort | `docs/superpowers/specs/2026-07-29-query-planner-hardening-design.md` |
| 2026-07-29 | Frontend slice #1: scaffold + dev-login + API/metadata client | `docs/superpowers/specs/2026-07-29-fe-scaffold-design.md` |
| 2026-07-30 | Frontend slice #2: `GeneratedList` | `docs/superpowers/specs/2026-07-30-generated-list-design.md` |
| 2026-07-31 | Dynamic role assignment (DB-backed roles, not JWT-carried) | `docs/superpowers/specs/2026-07-31-dynamic-role-assignment-design.md` |
| 2026-07-31 | Outbox transaction atomicity | `docs/superpowers/specs/2026-07-31-outbox-transaction-atomicity-design.md` |
| 2026-07-31 | Workflow Engine V1 | `docs/superpowers/specs/2026-07-31-workflow-engine-v1-design.md` |
| 2026-08-01 | Metadata Compiler (startup validation, hash/version, OpenAPI generation) | `docs/superpowers/specs/2026-08-01-metadata-compiler-design.md` |
| 2026-08-01 | Policy storage + RBAC/ABAC evaluator | `docs/superpowers/specs/2026-08-01-policy-storage-rbac-abac-design.md` |
| 2026-08-01 | Field-level + record-level permission enforcement | `docs/superpowers/specs/2026-08-01-field-record-enforcement-design.md` |
| 2026-08-01 | `PolicyExplainer` + `PermissionSnapshot` cache | `docs/superpowers/specs/2026-08-01-policy-explainer-snapshot-cache-design.md` |
| 2026-08-01 | Test DB separation + dependency audit | `docs/superpowers/specs/2026-08-01-test-db-separation-dependency-audit-design.md` |
| 2026-08-01 | Hot field index strategy (metadata-driven expression indexes) | `docs/superpowers/specs/2026-08-01-hot-field-index-strategy-design.md` |
| 2026-08-02 | Full-text search strategy (opt-in `tsvector`/GIN per field) | `docs/superpowers/specs/2026-08-02-full-text-search-strategy-design.md` |
| 2026-08-02 | Keyset pagination (opaque cursor over the resolved sort) | `docs/superpowers/specs/2026-08-02-keyset-pagination-design.md` |
| 2026-08-02 | List pagination + virtualization for `GeneratedList` | `docs/superpowers/specs/2026-08-02-list-pagination-design.md` |
| 2026-08-02 | Permission-aware UI state (proactive record capabilities) | `docs/superpowers/specs/2026-08-02-permission-aware-ui-design.md` |
| 2026-08-02 | List navigation + record delete (soft-delete) | `docs/superpowers/specs/2026-08-02-list-navigation-delete-design.md` |
| 2026-08-02 | Monorepo restructure: `packages/core` (pnpm workspace, backend extraction) | `docs/superpowers/specs/2026-08-02-monorepo-packages-core-design.md` |
| 2026-08-02 | Monorepo restructure: `packages/platform-react` + `apps/demo` | `docs/superpowers/specs/2026-08-02-monorepo-platform-react-design.md` |
| 2026-08-02 | Monorepo restructure: `apps/crm` (real module split, pulled forward) | `docs/superpowers/specs/2026-08-02-monorepo-apps-crm-design.md` |

All of the above are **Accepted** and implemented (see `docs/roadmap.md` for phase-level status).

## Notable decisions not covered by a dedicated spec

- **`core->modules` layering**: `packages/core` must never import a specific business entity — entity registration is an application-layer concern owned by each `apps/<module>` (e.g. `apps/crm/src/modules/registry.ts`, called after `createContainer()`/`buildApp()` return). Originally fixed as a convention during the Metadata Compiler work; now also a hard package boundary (`packages/core` has no dependency path to any `apps/*` package) since the 2026-08-02 monorepo restructure.
- **Index expression must match the query expression exactly**: an `IndexReconciler`-built index on `data->>'field'` is *never* selected by Postgres for a query written as `jsonb_extract_path_text(data, 'field')`, even though they're semantically equal — Postgres's expression-index matching is syntactic, not semantic. Every index this codebase builds uses `jsonb_extract_path_text`, matching `QueryPlanner`'s own filter/sort expression. Found and fixed during the Hot Field Index Strategy work.
- **Postgres DDL accepts no bind parameters at all** (not just under `CONCURRENTLY`) — `IndexReconciler` inlines entity/field names as escaped SQL literals, safe only because they come exclusively from server-authored, `MetadataCompiler`-validated metadata, never request input.
