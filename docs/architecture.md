# Architecture

Metap keeps a fast metadata-driven development model: declare metadata once, then get CRUD, list, workflow, audit, export, and UI metadata consistently.

The difference is that helpers are a facade, not the architecture. The platform is split into explicit services.

## High-level Layers

```txt
HTTP routes
  -> application services
    -> platform core
      -> repositories / database
      -> outbox
      -> RabbitMQ publisher
```

## Core Modules

### Metadata Registry

Owns entity definitions:

- fields
- list views
- validation schema
- workflow
- index/search/sort hints

Metap validates and compiles metadata as a first-class runtime artifact rather than treating it as a passive schema description.

### CRUD Service

Generic CRUD for metadata entities.

Responsibilities:

- validate data with Zod
- enforce permission through `PermissionService`
- call `QueryPlanner` for list/search
- persist records
- enqueue outbox events
- call `WorkflowEngine` where needed

### Permission Service

The permission layer will own:

- tenant scope
- user/function/action permission
- field-level permission
- record-level permission
- policy explanation/debugging

The early scaffold allows everything so the architecture can boot, but the service boundary is already fixed.

### Query Planner

The query planner turns safe view/query contracts into SQL.

Rules:

- every list has a max limit
- every business query includes tenant scope
- frontend cannot send arbitrary database query operators
- filter/sort fields must be declared in metadata
- expensive reports use dedicated report services or background jobs

### Workflow Engine

Workflow is metadata-driven:

- state field
- initial state
- terminal states
- transitions
- actions

Workflow changes should become atomic operations with optimistic locking. Side effects should be emitted after commit through outbox events.

### Outbox Service

API transactions write outbox rows in PostgreSQL. A publisher drains rows and publishes to RabbitMQ.

This protects the system from losing business events when RabbitMQ is temporarily unavailable.

## Data Model Strategy

Metap starts with a generic `records` table:

- stable columns for system-level fields
- `data jsonb` for metadata-driven business fields
- tenant/entity/status indexes
- version column for optimistic locking

This preserves metadata-driven development speed. Over time, high-volume or accounting-critical modules can get dedicated typed tables while still using the same metadata facade.

Recommended evolution:

```txt
Phase 1: generic records + JSONB
Phase 2: indexed generated columns for hot fields
Phase 3: dedicated tables for accounting/inventory critical paths
Phase 4: report/materialized views for heavy analytics
```

## Service Boundaries

Do not let HTTP, Drizzle, RabbitMQ, and metadata logic leak everywhere.

Allowed dependencies:

```txt
routes -> services
services -> metadata / permission / query / workflow / repositories / outbox
infra -> database / messaging
modules -> metadata definitions + optional custom handlers
```

Avoid:

- module code importing raw database client directly
- frontend query operators mapping directly to SQL
- workflow handlers publishing RabbitMQ directly
- authorization living only in frontend or gateway config

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

- Hard max page size.
- Keyset pagination for high-volume records.
- Background jobs for export/print/report.
- Query contracts per list view.
- Metadata and permission snapshot cache.
- Indexes declared close to metadata.
- Reporting workload separated from OLTP workload when needed.

## Target Architecture: Multi-Service Evolution

Metap is the backbone of a low-code platform, not a single-purpose ERP app. `src/core` (metadata, permission, query planner, workflow, outbox) is the reusable core platform. Each ERP subsystem — CRM, sales, inventory, accounting, and so on — is expected to eventually become its own independently deployable service built on that same core, not a copy of it.

This section is documentation-only: it records the target shape and the triggers for moving toward it. None of it is built yet, and none of it should be built ahead of its trigger.

**Anchor already in place:** `EntityDefinition.name` is already dot-namespaced by domain (`crm.customers`). The prefix before the dot is, in effect, the future service name. No code change is needed for this — just discipline: every new entity module goes under a domain-namespaced name so the future service boundary is legible in the data before any physical split happens.

**Repo/package layout.** Now: stays exactly as it is, one package. Target, once a second phân hệ is actually being built: a pnpm workspace with `packages/core` (today's `src/core` plus shared `src/infra`) and `apps/<phân-hệ>` (one thin Fastify app per service, each importing `packages/core` and registering only its own entity modules). Trigger: the first time a second, genuinely separate phân hệ needs to exist as its own deployable unit — not before.

**Data strategy.** Now, and for the foreseeable future: one shared PostgreSQL instance. Design constraint that keeps a later split cheap without being premature now: no code may join across different entities' data directly in SQL — `QueryPlanner` already only ever scopes a query by a single `entity` + `tenantId`, so this costs nothing to state, only to keep enforcing. If a future phân hệ needs another phân hệ's data, it goes through that phân hệ's service-level API — today that's an in-process call into the shared `CrudService`, but it should be treated as a remote call architecturally, so the eventual move to an actual remote call is a non-event. Splitting a phân hệ's data out to its own database later then becomes a matter of moving rows and a connection string, not rewriting query logic.

**Protocol strategy.** Now: REST, as already built (JWT auth, structured errors, CRUD with optimistic locking, metadata-constrained filter/sort). Future, triggered by having ≥2 services whose data a single frontend screen needs to aggregate: a GraphQL gateway acting as a BFF (backend-for-frontend) in front of the REST services, composing their responses for the frontend. Future, triggered by the repo/package split above actually having happened: gRPC as an option for service-to-service calls where REST's overhead matters. Neither is built or evaluated further until its trigger is real.
