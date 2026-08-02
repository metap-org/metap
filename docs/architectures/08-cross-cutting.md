# 8. Cross-cutting Concepts

Patterns and principles that apply across many building blocks, not owned by any single one.

## Metadata-Driven Development

Every entity's fields, list views, validation schema, workflow, and index/search hints are declared once (`EntityDefinition`) and compiled/validated as a runtime artifact (`MetadataCompiler`) rather than treated as passive config. See [05. Building Block View](05-building-blocks.md).

## Transactional Outbox

A business write and the event(s) it produces commit in the same PostgreSQL transaction; a separate publisher process drains and delivers them to RabbitMQ. This is the only mechanism through which side effects reach RabbitMQ — no service publishes directly. See [06. Runtime View](06-runtime.md).

## Multi-Tenancy

Every business table carries `tenant_id`; every `QueryPlanner`/`CrudService` call is scoped by it (`PermissionService.scopedTenant`). There is no cross-tenant query path anywhere in the codebase. `scopedTenant` takes a full `RequestContext` (not `Partial<RequestContext>`) and throws rather than silently falling back to a default tenant if `tenantId` is ever empty — an empty tenant at this point means a real bug upstream (the auth hook always derives a real `tenantId` from a verified JWT before any query-planning code runs), and a silent default would turn that bug into wrong-but-quiet cross-tenant-looking query results instead of a clear, loud failure. Fixed 2026-08-02 after an external architecture review flagged the old silent-fallback behavior — see [09. Architecture Decisions](09-adr.md).

## Permission Enforcement

RBAC (role allow-lists) combined with optional ABAC (attribute conditions), evaluated server-side, at three levels: entity-level (can this role touch this entity at all), field-level (which fields can be read/written), record-level (which specific rows can be read/written, translated into a SQL `WHERE` clause). See [05. Building Block View](05-building-blocks.md#permission-service).

## Security Principles

- Default business routes require auth.
- Tenant scope is mandatory.
- Permission is enforced server-side.
- CORS is allowlisted.
- Rich HTML must be sanitized before rendering.
- Secrets never live in repository.
- Containers should run non-root in production.
- Audit log must be append-only for sensitive actions.

## Performance Principles

- Hard max page size. (Done.)
- Keyset pagination for high-volume records. (Done — see [05. Building Block View](05-building-blocks.md#query-planner).)
- Background jobs for export/print/report. (Deferred, trigger-based — see [11. Risks and Technical Debt](11-risks.md).)
- Query contracts per list view. (Done.)
- Metadata and permission snapshot cache. (Done — `PermissionSnapshot`, per-call, deliberately not cross-request/TTL.)
- Indexes declared close to metadata. (Done — `EntityField.indexed`/`unique`/`searchMode`, reconciled by `IndexReconciler`.)
- Reporting workload separated from OLTP workload when needed. (Deferred, trigger-based — same item as above.)
