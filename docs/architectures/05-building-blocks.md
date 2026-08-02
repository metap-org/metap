# 5. Building Block View

## High-level Layers

```txt
HTTP routes
  -> application services
    -> platform core
      -> repositories / database
      -> outbox
      -> RabbitMQ publisher
```

## C4 Level 2: Containers

```mermaid
C4Container
  title Container diagram — Metap

  Person(user, "End User")
  Person(admin, "Admin")

  System_Boundary(metap, "Metap") {
    Container(web, "Web Frontend", "React, Vite, TanStack Query", "Dev harness SPA — apps/demo, consuming packages/platform-react via workspace:*")
    Container(api, "API Server", "Node.js, Fastify", "apps/crm: the one deployed module today, importing packages/core (auth, CRUD, metadata, admin, query planning)")
    Container(worker, "Outbox Publisher", "Node.js", "apps/crm/src/workers/outbox-publisher.ts, calling packages/core's runOutboxPublisherLoop()")
  }

  ContainerDb(db, "PostgreSQL", "Postgres 16", "records, metadata_versions, policies, outbox_events, workflow_events, user_roles")
  ContainerQueue(mq, "RabbitMQ", "AMQP 0-9-1", "Reliable event delivery to future downstream consumers")

  Rel(user, web, "Uses", "HTTPS")
  Rel(admin, web, "Uses", "HTTPS")
  Rel(web, api, "Calls", "REST/JSON, Bearer JWT")
  Rel(api, db, "Reads/writes records, metadata, policies; writes outbox rows in the same transaction as the business write", "Drizzle/SQL")
  Rel(worker, db, "Polls pending outbox rows", "SQL, 1s loop")
  Rel(worker, mq, "Publishes", "AMQP")
```

The API Server and the Outbox Publisher are deliberately separate processes (`pnpm dev` vs `pnpm worker:outbox`) — a RabbitMQ outage stalls the worker, never the API, because the transactional outbox write already committed.

## C4 Level 3: Components (inside the API Server)

```mermaid
C4Component
  title Component diagram — API Server

  Container_Boundary(api, "API Server") {
    Component(routes, "HTTP Routes", "Fastify handlers", "records / metadata / admin / health — packages/core/src/server/routes")
    Component(crud, "CrudService", "TypeScript class", "permission -> validate -> plan -> write -> workflow -> outbox")
    Component(metadata, "MetadataRegistry", "TypeScript class", "Entity definitions; validated + hashed at boot (MetadataCompiler)")
    Component(perm, "PermissionService", "TypeScript class", "RBAC/ABAC, field/record enforcement, PolicyExplainer")
    Component(query, "QueryPlanner", "TypeScript class", "Metadata-constrained filter/sort/cursor -> SQL")
    Component(workflow, "WorkflowEngine", "TypeScript class", "State machine transitions + audit log")
    Component(outbox, "OutboxService", "TypeScript class", "Transactional outbox writes")
    Component(idxr, "IndexReconciler", "TypeScript class", "Reconciles indexes from metadata at boot")
    Component(drift, "MetadataDriftService", "TypeScript class", "Warns on metadata hash drift across restarts")
  }

  ContainerDb(db, "PostgreSQL", "", "")

  Rel(routes, crud, "Calls")
  Rel(crud, metadata, "Reads entity definitions")
  Rel(crud, perm, "Checks permission, loads PermissionSnapshot")
  Rel(crud, query, "Plans list queries")
  Rel(crud, workflow, "Assigns initial status / runs transitions")
  Rel(crud, outbox, "Enqueues events (same DB transaction)")
  Rel(query, perm, "ANDs record-level policy WHERE clause")
  Rel(idxr, metadata, "Reads indexed / unique / searchMode flags")
  Rel(drift, metadata, "Reads entity hash (version)")
  Rel(crud, db, "Reads/writes", "Drizzle")
  Rel(idxr, db, "CREATE INDEX CONCURRENTLY", "DDL, best-effort")
```

## Logical View (class-level)

The object model behind the component diagram above — classes and how they depend on each other, not deployable units. (Kruchten 4+1's Logical View.)

```mermaid
classDiagram
  class Container {
    +db
    +auth
    +metadata: MetadataRegistry
    +permissions: PermissionService
    +queryPlanner: QueryPlanner
    +workflow: WorkflowEngine
    +outbox: OutboxService
    +crud: CrudService
    +indexReconciler: IndexReconciler
    +metadataDrift: MetadataDriftService
  }
  class MetadataRegistry {
    -entities: Map~string, EntityDefinition~
    +register(entity)
    +getEntity(name) EntityDefinition
    +listEntities() EntitySummary[]
    +validateReferences()
  }
  class EntityDefinition {
    +name: string
    +fields: EntityField[]
    +listViews: EntityListView[]
    +workflow?: EntityWorkflow
    +schema: ZodSchema
  }
  class CrudService {
    +list(entity, input, context)
    +create(entity, data, context)
    +update(entity, id, version, data, context)
    +transition(entity, id, action, version, context)
  }
  class PermissionService {
    +canReadEntity(context, entity)
    +canCreateEntity(context, entity)
    +canUpdateEntity(context, entity)
    +loadSnapshot(tenantId, entity) PermissionSnapshot
    +scopedTenant(context)
  }
  class PermissionSnapshot {
    +filterReadableFields(context, data)
    +assertWritableFields(context, fields, existing)
    +canUpdateRecordCondition(context, record)
    +getRecordPolicies(action)
  }
  class QueryPlanner {
    +planList(entity, input, context, policies) PlannedListQuery
  }
  class WorkflowEngine {
    +getInitialStatus(entity, data)
    +findTransition(entity, action, fromState)
    +runGuard(transition, data, context)
  }
  class OutboxService {
    +enqueue(executor, event)
    +publishPending(limit)
  }
  class IndexReconciler {
    +reconcile(entities, log)
  }
  class MetadataDriftService {
    +check(entities, log)
  }

  Container --> MetadataRegistry
  Container --> PermissionService
  Container --> QueryPlanner
  Container --> WorkflowEngine
  Container --> OutboxService
  Container --> CrudService
  Container --> IndexReconciler
  Container --> MetadataDriftService
  MetadataRegistry --> EntityDefinition : holds
  CrudService --> MetadataRegistry
  CrudService --> PermissionService
  CrudService --> QueryPlanner
  CrudService --> WorkflowEngine
  CrudService --> OutboxService
  PermissionService --> PermissionSnapshot : creates per call
  QueryPlanner --> PermissionService
  IndexReconciler --> MetadataRegistry
  MetadataDriftService --> MetadataRegistry
```

## Whitebox: Core Services

### Metadata Registry

Owns entity definitions:

- fields
- list views
- validation schema
- workflow
- index/search/sort hints

Metap validates and compiles metadata as a first-class runtime artifact rather than treating it as a passive schema description. `MetadataCompiler` enforces this at `MetadataRegistry.register()` time — duplicate fields, dangling listView field/filter/sort references, missing enum values, and malformed workflow shape all fail startup, not the first request. Each entity gets a deterministic hash of its shape (`MetadataCompiler.hash`, guard functions excluded) exposed as `version` on `GET /metadata/entities`; a `MetadataDriftService` compares that hash against the last-recorded one on every boot and warns — never crashes — on drift, mirroring `HealthService`'s graceful-degradation stance. The same safe metadata projection also drives a generated OpenAPI document at `GET /metadata/openapi.json`.

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

The permission layer owns:

- tenant scope
- role assignment — dynamic, DB-backed per `(tenantId, userId)`, granted/revoked at runtime through an admin API (`RoleAssignmentService`); the JWT itself is a bare identity assertion, not a role carrier
- policy storage — a role allow-list combined with an optional attribute condition (`PolicyCondition`), OR-combined across matching policies, no deny rules
- field-level permission — read masking and write gating, wired into every `CrudService` call site (`list`/`create`/`update`/`transition`)
- record-level permission — attribute conditions translated into a `WHERE` clause (`condition-to-sql.ts`) and ANDed into `QueryPlanner.planList` for reads, plus a same-shape check before writes
- policy explanation/debugging — `PolicyExplainer` produces a read-only trace of every policy considered and why, exposed via an admin-gated simulator endpoint
- a per-call `PermissionSnapshot` batches a tenant/entity's policies into one DB fetch reused across a single `CrudService` call — deliberately not a cross-request/TTL cache

Started as a scaffold that allowed everything so the architecture could boot; the service boundary was fixed from day one and the real logic above now fills it in.

### Query Planner

The query planner turns safe view/query contracts into SQL.

Rules:

- every list has a max limit
- every business query includes tenant scope
- frontend cannot send arbitrary database query operators
- filter/sort fields must be declared in metadata
- expensive reports use dedicated report services or background jobs (deferred, trigger-based — see [11. Risks and Technical Debt](11-risks.md))

Built on top of that baseline:

- **Hot field indexes.** `EntityField.indexed`/`unique` drive `IndexReconciler`, which reconciles per-entity partial expression indexes on `records` automatically at boot (`CREATE INDEX CONCURRENTLY IF NOT EXISTS`, best-effort) and via a manual `pnpm index:reconcile` script. The indexed expression must byte-for-byte match the query's own filter/sort expression (`jsonb_extract_path_text`, not the semantically-equivalent `->>` operator) or Postgres never selects it.
- **Full-text search.** `EntityField.searchMode: "fts"` (opt-in; default stays substring/ILIKE) matches via `to_tsvector('simple', ...) @@ plainto_tsquery('simple', ...)`, backed by a GIN index — same `IndexReconciler` mechanism as above.
- **Keyset pagination.** An opaque, base64-encoded cursor (never interpreted by the client) is validated against the *resolved* sort (post-fallback) and turned into a keyset `WHERE` condition; a cursor for the wrong sort, or a malformed one, is a `400`, never silently accepted or a `500`.

### Workflow Engine

Workflow is metadata-driven:

- state field
- initial state
- terminal states
- transitions
- actions

Transitions are atomic operations with optimistic locking (a version-mismatch write fails the request, not the state), guarded by plain TypeScript predicates on `WorkflowTransition`. Every transition is logged to an append-only `workflow_events` audit table and emits a `<entity>.workflow.transitioned` outbox event after commit — side effects only ever flow through the outbox, never a direct publish.

### Outbox Service

API transactions write outbox rows in PostgreSQL. A publisher drains rows and publishes to RabbitMQ.

This protects the system from losing business events when RabbitMQ is temporarily unavailable.

## Data Model

Metap starts with a generic `records` table:

- stable columns for system-level fields
- `data jsonb` for metadata-driven business fields
- tenant/entity/status indexes
- version column for optimistic locking

This preserves metadata-driven development speed. Over time, high-volume or accounting-critical modules can get dedicated typed tables while still using the same metadata facade.

Recommended evolution:

```txt
Step 1: generic records + JSONB (done)
Step 2: metadata-driven indexes for hot fields (done — see Query Planner
        above; shipped as per-entity partial expression indexes generated
        by IndexReconciler, not physical generated columns — a shared
        `records` table can't grow one column per possible field name
        across every entity without its column count growing unboundedly)
Step 3: dedicated tables for accounting/inventory critical paths
Step 4: report/materialized views for heavy analytics
```

Steps 3-4 are not built and have no trigger yet — see [11. Risks and Technical Debt](11-risks.md).

### Database Design (ER diagram)

Six tables, no cross-table foreign key constraints — `tenant_id`/`entity`/`aggregate_id`/`record_id` are plain columns whose relationships are enforced by application code (`QueryPlanner`, `CrudService`), not the database schema. This is deliberate: `records` is one generic, entity-agnostic table, so a real FK from e.g. `workflow_events.record_id` to `records.id` would work today but would have to be dropped the moment any single entity gets peeled off into its own dedicated table (Step 3 above) — not before its trigger.

```mermaid
erDiagram
  RECORDS {
    uuid id PK
    uuid tenant_id
    varchar entity
    varchar code
    varchar status
    jsonb data
    integer version
    boolean deleted
    timestamptz created_at
    timestamptz updated_at
    uuid created_by
    uuid updated_by
  }
  OUTBOX_EVENTS {
    uuid id PK
    varchar topic
    varchar aggregate_type
    uuid aggregate_id
    jsonb payload
    timestamptz published_at
    integer attempts
    text last_error
    timestamptz created_at
  }
  WORKFLOW_EVENTS {
    uuid id PK
    uuid tenant_id
    varchar entity
    uuid record_id
    varchar action
    varchar from_state
    varchar to_state
    uuid actor
    timestamptz created_at
  }
  USER_ROLES {
    uuid id PK
    uuid tenant_id
    uuid user_id
    varchar role
    timestamptz created_at
    uuid created_by
  }
  POLICIES {
    uuid id PK
    uuid tenant_id
    varchar entity
    varchar action
    varchar field
    varchar subject
    jsonb roles
    jsonb condition
    timestamptz created_at
    uuid created_by
  }
  METADATA_VERSIONS {
    varchar entity_name PK
    varchar hash
    timestamptz updated_at
  }

  RECORDS ||--o{ OUTBOX_EVENTS : "aggregate_id (app-enforced)"
  RECORDS ||--o{ WORKFLOW_EVENTS : "record_id (app-enforced)"
  RECORDS }o--|| METADATA_VERSIONS : "entity (app-enforced)"
  POLICIES }o--|| METADATA_VERSIONS : "entity (app-enforced)"
  USER_ROLES }o--o{ POLICIES : "roles (JSONB array, matched at query time)"
```

Notes:

- `records.data` is the metadata-driven payload; `code`/`status` are denormalized top-level columns that mirror two fields inside `data` (`code` always, `status` mirrors `entity.workflow.stateField`'s value) purely so they can be indexed/queried as real columns.
- `outbox_events`/`workflow_events` reference `records` rows by id (`aggregate_id`/`record_id`) but across the *whole* generic table, not a per-entity table — one outbox table serves every entity.
- `policies.roles` is a JSONB array matched against a caller's roles at evaluation time (`roleGatePassed`), not a relational join to `user_roles`.
- Real indexes beyond the primary keys shown above are covered in "Hot field indexes"/"Full-text search" above — those are per-entity partial expression indexes generated from metadata, not part of this fixed schema.

## Service Boundaries

Do not let HTTP, Drizzle, RabbitMQ, and metadata logic leak everywhere.

Allowed dependencies:

```txt
routes -> services
services -> metadata / permission / query / workflow / repositories / outbox
infra -> database / messaging
apps/<module> -> packages/core (via workspace:*) — never the other way around
```

Avoid:

- module code importing raw database client directly
- frontend query operators mapping directly to SQL
- workflow handlers publishing RabbitMQ directly
- authorization living only in frontend or gateway config

### Development View (workspace package organization)

The same dependency rule above, visualized as pnpm workspace packages (Kruchten 4+1's Development View). Each box is a real package with its own `package.json`, not just a source-tree folder — as of the 2026-08-02 monorepo restructure, these boundaries are enforced by pnpm's isolated `node_modules`, not only by convention.

```mermaid
graph TD
  subgraph pkgcore["packages/core (@metap/core) — entity-agnostic library"]
    routes["src/server/routes<br/>+ src/server/app.ts (buildApp(config, entities))"]
    core["src/core<br/>crud, metadata, permission, query, workflow, outbox"]
    infra["src/infra<br/>db (Drizzle), messaging (RabbitMQ)"]
    loop["src/workers/outbox-publisher-loop.ts<br/>runOutboxPublisherLoop() — reusable"]
  end

  subgraph appscrm["apps/crm (@metap/crm) — the one deployed module today"]
    modules["src/modules<br/>customer.entity.ts + registry.ts"]
    entry["src/main.ts<br/>+ src/workers/*.ts (thin entry points)"]
  end

  subgraph pkgplatform["packages/platform-react (@metap/platform-react)"]
    platform["GeneratedList/Form, FieldValue/Input,<br/>WorkflowActionBar, RecordDetail, api-client"]
  end

  subgraph appsdemo["apps/demo (@metap/demo)"]
    demoapp["src/App.tsx, src/demo/*<br/>React + Vite + TanStack Query"]
  end

  routes --> core
  core --> infra
  entry -->|"workspace:*"| routes
  entry -->|"workspace:*"| loop
  modules -.entity definitions, no core import.-> entry
  demoapp -->|"workspace:*"| platform
  demoapp -.HTTP only, never imports packages/core.-> routes
```

`apps/crm` depends on `packages/core`; `packages/core` has no dependency path back to `apps/crm` or any other `apps/*` package — that direction is what keeps `packages/core` genuinely entity-agnostic, not just conventionally so. `apps/demo` is the frontend's equivalent: it can only ever reach the backend over HTTP (the dotted line), never by importing backend code, and it consumes `packages/platform-react` the same way `apps/crm` consumes `packages/core`.
