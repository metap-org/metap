# Metadata Compiler — Design

Date: 2026-08-01
Status: Proposed, pending review

## Context

Roadmap Phase 2, next in sequence after Phase 3 (Permission Engine, done —
see `docs/superpowers/specs/2026-08-01-{policy-storage-rbac-abac,field-record-enforcement,policy-explainer-snapshot-cache}-design.md`).

Today `MetadataRegistry` (`src/core/metadata/metadata-registry.ts`) is a
passive in-memory map: `register()` only throws on a duplicate entity
`name`, and `toMetadata()` is a hand-written wire projection (`label`,
`fields`, `listViews`, `workflow` with `guard` functions stripped) that does
zero validation of the shape it's given. Entity authors
(`src/modules/crm/customer.entity.ts`) declare `fields`, `listViews`, and
`workflow` independently of the Zod `schema` — nothing checks that
`listViews[].fields`/`.filters` reference real field names, that
`workflow.stateField` is an actual field, that every `kind: "enum"` field
declares `enumValues`, or that two transitions from the same state don't
share an `action`. A typo here fails silently (a filter on a non-existent
field is just ignored by `QueryPlanner`) or crashes later, deep inside
`CrudService`/`QueryPlanner`/`WorkflowEngine`, instead of at boot.

`docs/architecture.md`'s Metadata Registry section already claims "Metap
validates and compiles metadata as a first-class runtime artifact rather
than treating it as a passive schema description" — today that's aspirational,
not true. This spec is what makes it true.

Unlike Phase 3 (split into 4 review cycles because each sub-project was
independently risky and sequenced), this spec covers all of Phase 2's
roadmap goals in one cycle — each piece below is small, low-risk, and none
blocks on the others landing first.

## Goals

- Validate every registered `EntityDefinition` at startup and crash boot
  (not the first request) if it's incoherent — new `MetadataCompiler`
  (`src/core/metadata/metadata-compiler.ts`) and `MetadataValidationError`.
- Compute a stable version hash per entity from its compiled metadata,
  exposed as an additive `version` field alongside `label`/`fields`/`listViews`
  at `GET /metadata/entities` and `/metadata/entities/:entity`.
- Detect metadata drift across restarts: persist each entity's last-seen
  hash in a new DB table, and log a warning (never crash) on boot when it
  changes shape from what was last recorded.
- Generate an OpenAPI document for the generic CRUD/list/transition routes,
  one concrete path per entity, derived from each `EntityDefinition`'s field
  metadata — served at a new `GET /metadata/openapi.json` route.

## Non-goals

- No response-body examples or runtime request-validation changes — routes
  already validate via Zod; this only adds a discoverable schema document.
- No breaking-change *blocking*. The compatibility check only warns;
  refusing to boot on drift needs a real multi-instance deploy story first
  (ties to Phase 9's triggers, not fired yet).
- No UI changes. `GeneratedList`'s consumption of `/metadata/entities`
  (`EntitySummary`/`EntityField` in `web/src/platform/metadata/types.ts`)
  keeps working unchanged — `version` is additive and optional.
- No auto-derivation of `EntityField.required`/`searchable`/etc. from the
  Zod `schema`, despite the duplication visible between `CustomerSchema`
  and `fields: [...]` in `customer.entity.ts`. Collapsing that is a
  separate, larger refactor touching every entity module and isn't required
  by any Phase 2 roadmap goal — flagged as a candidate follow-up, not
  attempted here.
- No per-tenant metadata. Metadata is process-wide (compiled from code at
  boot, identical for every tenant), so the new `metadata_versions` table
  has no `tenantId` column — this is a deliberate asymmetry from every
  other new table added in Phase 3.

## Design

### 1. `MetadataCompiler.validate` + `MetadataValidationError`

New `src/core/metadata/metadata-compiler.ts`. `MetadataRegistry.register()`
calls `MetadataCompiler.validate(entity)` before storing it; a failure
throws `MetadataValidationError` (extends `Error`, carries `entity: string`
and `issues: string[]`). Since `createContainer` runs synchronously at
process start before Fastify begins listening, this is a boot-time crash by
design, not a request-time error path.

Per-entity checks (don't need the full registry):
- No duplicate `name` within `fields`.
- Every `listViews[].fields`, `.filters`, and `.defaultSort` (stripping a
  leading `-` for descending sort) names a field present in `fields`.
- Every `kind: "enum"` field declares a non-empty `enumValues`.
- Every `kind: "reference"` field declares a non-empty `refEntity` (the
  *target* entity's existence is checked separately, below — entities can
  register in any order, so a single-entity check can't verify it).
- If `workflow` is present: `stateField` names a real field; `initialState`,
  every `terminalStates` entry, and every `transitions[].from`/`.to` are
  non-empty strings; no two `transitions` share the same `(from, action)`
  pair (that would make `WorkflowEngine`'s transition lookup ambiguous).

Cross-entity check, `MetadataRegistry.validateReferences()`: called once
from `createContainer` (`src/core/container.ts`) after every
`metadata.register(...)` call, not per-registration — checks every
`refEntity` names an entity that ended up registered. Needs the complete
registry, unlike the checks above.

### 2. Metadata version hash

`MetadataCompiler.hash(entity: EntityDefinition): string` — a SHA-256 hex
digest (Node's `crypto` module) over a canonical JSON serialization of the
same shape `toMetadata()` produces (`label`, `fields`, `listViews`,
`workflow` with `guard` stripped — guards are functions, already
unrepresentable and already excluded from the wire projection). Canonical
serialization needs a `stableStringify` helper (recursively sort object
keys) since plain `JSON.stringify`'s key order follows insertion order,
which is stable per-process but not a documented cross-version guarantee —
relying on it for a hash meant to detect real drift would be fragile.

`MetadataRegistry.toMetadata()` adds the result as `version` on the
returned object. `EntitySummary` (`web/src/platform/metadata/types.ts`)
gains an optional `version?: string` — additive, no existing frontend
consumer breaks.

### 3. Compatibility check against last-seen hash

New table `metadata_versions`: `entityName text primary key`,
`hash text not null`, `updatedAt timestamptz not null`. Global, not
tenant-scoped (see Non-goals). Migration via `pnpm db:generate`.

Hooked into `buildApp` (`src/server/app.ts`), immediately after
`const container = createContainer(config)`: for each entity in
`container.metadata.listEntities()`, read the last-stored row by
`entityName`, and if the hash differs (or no row exists yet) log a `pino`
warning via `app.log.warn` (`"metadata drift detected for <entity>"` with
old/new hash, or `"first boot, recording initial hash"` when no row
exists), then upsert the new hash. `buildApp` is already `async` and this
runs before `app.listen(...)` in `src/main.ts` — no new lifecycle hook
needed.

### 4. OpenAPI generation

New `src/core/metadata/openapi-generator.ts`:
`generateOpenApiDocument(entities: EntitySummary[]): OpenApiDocument` (a
plain JSON-serializable object, `openapi: "3.1.0"`). For each entity,
generates concrete path items for the four existing generic routes —
`/api/{entity}` (GET list, POST create), `/api/{entity}/{id}` (PATCH
update), `/api/{entity}/{id}/transitions/{action}` (POST) — instantiated
per entity name (e.g. `/api/crm.customers`) rather than left as the
literal `:entity` template, so each entity's request/response shape can
differ.

Field `kind` → JSON Schema type is mapped through a new explicit table,
`FIELD_KIND_TO_JSON_SCHEMA`, in the same file — the first place this
mapping becomes shared and named rather than assumed ad hoc (`GeneratedList`
today informally assumes a similar mapping when choosing `Select` vs
`TextInput` per `kind`, without naming it as such).

Served at `GET /metadata/openapi.json` via a small addition to
`registerMetadataRoutes` (`src/server/routes/metadata.ts`) — this runs
inside `buildApp`'s `protectedApp` block (`src/server/app.ts`) alongside
`/metadata/entities` today, so it requires the same Bearer auth as every
other route there; no new public route is introduced by this spec.

## Consequences for existing code

- `MetadataRegistry.register()` gains a validation call and can now throw —
  `container.ts`'s `metadata.register(customerEntity)` becomes a possible
  boot-time failure point. Intended: catches entity-authoring mistakes at
  the earliest possible point, not the first request that touches them.
- `createContainer` (`src/core/container.ts`) must call
  `metadata.validateReferences()` once, after all `register()` calls.
- `buildApp` (`src/server/app.ts`) gains an async DB read/write between
  `createContainer` and route registration (the compatibility check) —
  small added boot latency (one query per entity), acceptable at this
  entity count.
- New migration for `metadata_versions` (`pnpm db:generate`).
- `docs/architecture.md`'s existing claim about metadata being "a
  first-class runtime artifact" becomes accurate; no wording change needed
  there once this ships.

## Open items for implementation plan

- Confirm `pnpm db:generate` picks up `metadata_versions` cleanly alongside
  the existing `records`/`outbox_events`/`policies`/`user_roles` tables in
  `schema.ts` — should be additive, but the plan should include a
  `pnpm db:generate && pnpm db:migrate` step and inspect the generated SQL
  before trusting it.
- `OpenApiDocument`'s type: confirmed no OpenAPI/Swagger package exists in
  `package.json` today (checked directly, not assumed) — the plan writes a
  minimal hand-rolled type for just the fields this generator emits, no new
  dependency.
- Exact wording/format for the drift warning log line, and whether it
  should also surface in `GET /health` (e.g. a `metadataDrift: string[]`
  array) for operational visibility — not required by the roadmap goal as
  stated, worth a quick decision in the plan rather than silently deciding
  either way here.
