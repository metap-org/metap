Status: written 2026-08-02, still the only spec for Phase 11 / Phase A sub-project 1
(`docs/roadmap.md`), not yet implemented (no plan, no code). Relocated from
`docs/superpowers/specs/` on 2026-08-07 when that directory was deleted (see
`docs/architectures/09-adr.md`) — this is the one item under `docs/superpowers/` that
wasn't already shipped, so its content is preserved here rather than dropped.

**Predates the 2026-08-07 decision to move `packages/core` to Rust**
(`docs/rust-core-viability.md`). The design below (data model, draft/publish/rollback
service contract, the three locked-in scoping decisions) is still valid — none of it is
TypeScript-specific in substance. The concrete file paths and Zod code sample under
"Data model"/"Service" are from the old TS layout and need re-targeting to Rust (following
`docs/rust-core-viability.md`'s Migration Order — this sub-project belongs after step 3,
Metadata layer) when this is actually planned and built, not taken as literal file paths
to create.

---

# Low-code Metadata Storage & Versioning Design

## Problem

Metap's stated higher destination (`docs/vision.md`) is to become a low-code platform: operators define, publish, and govern business applications from metadata, without editing source code for the standard path. `docs/low-code-platform-v1.md` already lays out a concrete, phased path — Phase A ("Metadata Control Plane Foundation"), Phase B ("Builder UI and Safe Runtime Rules"), Phase C ("Platform Hardening") — but that document is intentionally directional ("Status: exploratory"), not an implementation-ready spec.

This spec covers the first concrete slice of Phase A: **persisted metadata storage with draft/published versioning**, decomposed as the first of four ordered sub-projects that together deliver Phase A:

1. **Persisted metadata storage + draft/published versioning** (this spec).
2. Runtime loader — materialize published metadata through the existing `MetadataCompiler`/`MetadataRegistry` pipeline, proving `CrudService`/`QueryPlanner` work unchanged against DB-authored entities.
3. Publish validation pipeline — deeper semantic validation (cross-entity reference checks against the merged code+DB registry) gating every publish/rollback, once sub-project 2's merged registry exists to check against.
4. Metadata admin API — the HTTP surface an eventual builder UI calls.

Each sub-project gets its own spec → plan → implementation cycle, matching this project's established practice for multi-part efforts (e.g. the DB-coupling risk work, the frontend-architecture work, both done earlier in the same overall project).

## Decisions already made (recorded here so this spec doesn't re-litigate them)

- **DB-authored entities will eventually fully replace code-authored `*.entity.ts` files** — this is the stated direction, not a permanent dual-source architecture. `crm.customers` is not migrated as part of Phase A; it keeps working as a code-authored entity while the DB-authored path is built and proven on new entities first. `MetadataRegistry` will need to merge both sources for a transition period (sub-project 2's concern), but that merge is scaffolding for the migration, not a permanent feature.
- **Global metadata, not per-tenant, for Phase A.** DB-authored entities are managed platform-wide (one definition, visible to every tenant), matching how code-authored entities work today. Per-tenant custom entity definitions are a materially bigger architectural change (a load-once-at-boot `MetadataRegistry` would need to become dynamic/per-request) and are explicitly deferred past Phase A.
- **No workflow support for DB-authored entities in Phase A.** `WorkflowTransition.guard` is a TypeScript function today; DB-authored entities have no code to write one in. Phase B's declarative-rule work is the prerequisite for DB-authored workflow, so Phase A's persisted definition shape has no `workflow` key at all — not even a guard-less one, to avoid shipping a half-feature that needs reshaping later.

## Scope of this sub-project

**In scope:** a storage/versioning service, its DB schema, and shape-level (Zod) validation — usable and independently testable without touching `MetadataRegistry`, `buildApp`, or any HTTP route.

**Out of scope (explicitly deferred to later sub-projects):**
- Wiring into boot/`MetadataRegistry` (sub-project 2).
- Cross-entity reference validation (e.g. a DB-authored `"reference"`-kind field's `refEntity` pointing at a real, existing entity) — sub-project 3, once a merged registry exists to validate against.
- Any HTTP endpoint (sub-project 4).
- Preventing a draft's `name` from colliding with an already-registered code-authored entity name (e.g. `"crm.customers"`) — this needs `MetadataRegistry` knowledge this service deliberately doesn't have; deferred to whichever of sub-project 2/4 first has both pieces in hand.

## Data model

Reuses `EntityFieldSchema`/`EntityListViewSchema` (already defined in `packages/core/src/core/metadata/entity-wire-schema.ts`, already the source of truth for what crosses the wire) as the shape of a persisted definition's `fields`/`listViews` — no new, parallel field-shape modeling.

**New file `packages/core/src/core/metadata/low-code-entity-schema.ts`:**

```ts
import { z } from "zod";
import { EntityFieldSchema, EntityListViewSchema } from "./entity-wire-schema";

export const LowCodeEntityDefinitionSchema = z.object({
  name: z.string().min(1),
  label: z.string().min(1),
  fields: z.array(EntityFieldSchema),
  listViews: z.array(EntityListViewSchema),
});

export type LowCodeEntityDefinition = z.infer<typeof LowCodeEntityDefinitionSchema>;
```

**Two new tables in `packages/core/src/infra/db/schema.ts`** (naming deliberately distinct from the existing `metadata_versions` table, which is an unrelated boot-time drift-detection cache for code-authored entities — see `MetadataDriftService` — not a versioning store):

- **`low_code_entity_drafts`** — one row per entity name currently being authored. The mutable, in-progress copy; overwritten on every save.
  - `entityName` (`varchar`, primary key)
  - `definition` (`jsonb`, not null) — a `LowCodeEntityDefinition`
  - `updatedAt` (`timestamp with time zone`, not null, default now)

- **`low_code_entity_versions`** — append-only publish history. Rows are never updated or deleted; rollback creates a new row rather than rewriting one.
  - `id` (`uuid`, primary key, default random)
  - `entityName` (`varchar`, not null, indexed)
  - `definition` (`jsonb`, not null) — a `LowCodeEntityDefinition` snapshot at publish time
  - `versionNumber` (`integer`, not null) — increments per `entityName`, starting at 1
  - `publishedAt` (`timestamp with time zone`, not null, default now)
  - `restoredFromVersion` (`integer`, nullable) — set when this version was created by a rollback, naming the version number it restored; `null` for an ordinary publish
  - Unique constraint on `(entityName, versionNumber)`

Rollback restoring version 3 doesn't delete versions 4-5 or resurrect version 3's row — it creates version 6 with version 3's content and `restoredFromVersion: 3`, keeping history strictly append-only and audit-friendly (same "never mutate the past" instinct as this project's existing `workflow_events` append-only audit log).

## Service

**New file `packages/core/src/core/metadata/metadata-draft-service.ts`:**

```ts
export class MetadataDraftNotFoundError extends Error {}

export class MetadataDraftService {
  constructor(private readonly db: Database) {}

  async saveDraft(entityName: string, definition: LowCodeEntityDefinition): Promise<void>;

  async getDraft(entityName: string): Promise<LowCodeEntityDefinition | undefined>;

  async publish(entityName: string): Promise<{ versionNumber: number }>;

  async rollback(entityName: string, toVersionNumber: number): Promise<{ versionNumber: number }>;

  async getPublished(
    entityName: string,
  ): Promise<{ versionNumber: number; definition: LowCodeEntityDefinition } | undefined>;

  async listVersions(
    entityName: string,
  ): Promise<
    { versionNumber: number; publishedAt: Date; restoredFromVersion: number | null }[]
  >;
}
```

Behavior:

- **`saveDraft`** — validates `definition` against `LowCodeEntityDefinitionSchema` (throws Zod's own error on failure, no custom wrapper needed at this layer), then upserts into `low_code_entity_drafts` (insert-or-update on `entityName`).
- **`getDraft`** — returns the current draft row's definition, or `undefined` if none exists (a brand-new entity with nothing saved yet).
- **`publish`** — reads the current draft; throws `MetadataDraftNotFoundError` if there isn't one (nothing to publish). Re-validates against `LowCodeEntityDefinitionSchema` (defense in depth — a draft could in principle have been written before a schema tightening). Computes the next `versionNumber` as `1 + (max existing versionNumber for this entityName, or 0)`, inserts a new `low_code_entity_versions` row with `restoredFromVersion: null`, and returns the new version number. Does not clear or modify the draft row — draft and the newly-published version are identical content immediately after publish, which is the correct "no pending changes" state; further edits diverge from there naturally.
- **`rollback`** — reads the target `(entityName, toVersionNumber)` version row; throws `MetadataDraftNotFoundError` if it doesn't exist. Upserts its definition into the draft row (so an eventual builder UI shows the restored content as the live draft), computes the next `versionNumber` the same way `publish` does (continuing the monotonic sequence — rollback never reuses or rewinds a version number), and inserts a new version row with `restoredFromVersion: toVersionNumber`.
- **`getPublished`** — the highest-`versionNumber` row for `entityName`, or `undefined` if never published.
- **`listVersions`** — all versions for `entityName`, ordered newest-first, for an eventual history/rollback UI.

No cross-entity validation (e.g. checking a `"reference"`-kind field's `refEntity` names a real entity) happens here — `saveDraft`/`publish` only validate the shape of the definition being saved in isolation. Sub-project 3 layers the deeper, registry-aware validation on top of `publish`/`rollback` once a merged registry exists to validate against (sub-project 2).

## Testing

TDD, following this project's established backend discipline (failing test first, verified red, then implementation, verified green) and its integration-test conventions (real Postgres via the existing test DB setup, not mocks — this project's tests consistently hit a real database).

`packages/core/src/core/metadata/metadata-draft-service.test.ts` — new file, covering:
- `saveDraft` then `getDraft` round-trips the definition.
- `saveDraft` with an invalid shape (e.g. a field missing `kind`) rejects via the Zod schema.
- `publish` with no draft throws `MetadataDraftNotFoundError`.
- `publish` after `saveDraft` creates version 1; a second `publish` after another `saveDraft` creates version 2.
- `getPublished` returns the latest version's content and number.
- `listVersions` returns all versions, newest first.
- `rollback` to a nonexistent version throws `MetadataDraftNotFoundError`.
- `rollback` to an existing older version creates a new version with `restoredFromVersion` set to the target, and updates the draft to match.

## File summary

- Create: `packages/core/src/core/metadata/low-code-entity-schema.ts`
- Create: `packages/core/src/core/metadata/metadata-draft-service.ts`
- Create: `packages/core/src/core/metadata/metadata-draft-service.test.ts`
- Modify: `packages/core/src/infra/db/schema.ts` (add `lowCodeEntityDrafts`, `lowCodeEntityVersions` tables)
- Generated: a new Drizzle migration (`pnpm db:generate` + `pnpm db:migrate`, `packages/core`'s established workflow)
