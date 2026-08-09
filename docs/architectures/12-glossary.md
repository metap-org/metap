# 12. Glossary

| Term | Meaning |
|---|---|
| **Entity** | A business object type declared once as an `EntityDefinition` (a Rust module, e.g. `apps/crm-server/src/customer_entity.rs`) — fields, list views, workflow. No dedicated database table; stored in the generic `records` table. |
| **`records` table** | The single generic table every entity's data lives in — tenant/entity/status/code columns plus a `data jsonb` column for the metadata-driven fields. |
| **Tenant** | An isolation boundary; every business row, query, and permission check is scoped by `tenant_id`. |
| **Outbox pattern** | Writing an event to a DB table in the same transaction as the business change, then draining it to RabbitMQ (through the `EventBus` trait) from a separate process (`outbox-publisher`) — avoids losing events on broker downtime. |
| **`EventBus`** | A `metap-infra` trait events are published through (`RabbitEventBus` its only implementation) — the seam that would let a future broker (Kafka, NATS, ...) be swapped in behind `outbox-publisher` without touching the services that enqueue events. |
| **`MetadataCompiler`** | Validates an `EntityDefinition` at registration time (duplicate fields, dangling references, malformed workflow) and computes a deterministic hash of its shape. |
| **`MetadataDriftService`** | Compares an entity's current metadata hash against the last-recorded one at every boot; warns (never crashes) on drift. |
| **`IndexReconciler`** | Reads `EntityField.indexed`/`unique`/`searchMode` and creates matching partial expression / GIN indexes on `records`, idempotently, at boot and via a manual reconcile invocation. |
| **RBAC** | Role-Based Access Control — a policy grants access based on the caller's assigned roles. |
| **ABAC** | Attribute-Based Access Control — a policy's grant additionally depends on an attribute condition (`PolicyCondition`), evaluated against the request context or the record. |
| **`PermissionSnapshot`** | A per-`CrudService`-call batch of a tenant/entity's policies, loaded once and reused — not a cross-request cache. |
| **`PolicyExplainer`** | Produces a read-only trace of every policy considered for a hypothetical request, for admin-facing debugging. |
| **Keyset pagination** | Paging by "give me rows after this cursor value," not by numeric offset — stays efficient on large tables and stable under concurrent inserts, unlike `OFFSET`. |
| **Cursor** | An opaque, base64-encoded token encoding the last row's sort field value + id + sort direction, used to fetch the next page. |
| **`searchMode: "fts"`** | Opt-in per-field flag switching a field's filter match from substring (`ILIKE`) to real Postgres full-text search (`tsvector`/`plainto_tsquery`). |
| **Workflow transition** | A guarded (a `PolicyCondition`, not a function), atomic state change on an entity's `stateField`, logged to `workflow_events` and emitted as an outbox event. |
| **C4 Model** | Simon Brown's four-level structural diagram notation: Context → Container → Component → Code. Used here for Context ([03](03-context.md)), Container + Component ([05](05-building-blocks.md)). |
| **4+1 View Model** | Philippe Kruchten's five-viewpoint description of a system: Logical, Process, Development, Physical, plus Scenarios. Folded into this document's arc42 sections — Logical + Development into [05](05-building-blocks.md), Process + Scenarios into [06](06-runtime.md), Physical into [07](07-deployment.md). |
| **arc42** | A 12-section documentation template for software architecture (not a diagram notation) — the section structure this whole `docs/architectures/` folder follows. |
| **ADR** | Architecture Decision Record — this project records decisions directly in [09](09-adr.md) (previously via a `docs/superpowers/specs/*.md` design-spec workflow, retired 2026-08-07). |
