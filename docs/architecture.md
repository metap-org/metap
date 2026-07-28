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
