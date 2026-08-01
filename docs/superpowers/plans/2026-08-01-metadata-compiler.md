# Metadata Compiler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `MetadataRegistry` from a passive in-memory map into a real compiler: validate every `EntityDefinition` at startup, compute a stable version hash per entity, detect metadata drift across restarts, and generate an OpenAPI document for the generic CRUD routes — closing out roadmap Phase 2.

**Architecture:** A new `MetadataCompiler` (`src/core/metadata/metadata-compiler.ts`) does per-entity structural validation and hashing; `MetadataRegistry` calls it from `register()` (per-entity checks) and gains a new `validateReferences()` for the one cross-entity check (`refEntity` existence). A new `metadata_versions` table persists last-seen hashes; `buildApp` compares and warns on drift right after building the container. A new `openapi-generator.ts` turns the registry's entities into a minimal OpenAPI 3.1 document served at `GET /metadata/openapi.json`.

**Tech Stack:** TypeScript, Node `crypto` (SHA-256), Drizzle ORM/PostgreSQL, Fastify, Zod, vitest.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-01-metadata-compiler-design.md`.
- Validation failures throw `MetadataValidationError` and crash boot — this is intentional (catch entity-authoring mistakes at the earliest point), not a request-time error path. Do not soften this to a warning.
- The drift check (Task 5) only ever warns, never blocks boot — do not make it throw.
- `metadata_versions` is a global table, no `tenantId` column — metadata is process-wide, identical for every tenant. Don't copy the `tenantId`-scoped pattern from `policies`/`user_roles`.
- No new runtime dependency for OpenAPI — hand-roll the minimal type used (confirmed no existing OpenAPI/Swagger package in `package.json`).
- Per this project's standing rule, do not run `git commit` — leave the diff for review.
- Per this project's established test convention: minimal targeted tests per behavior, not exhaustive matrices — one or two focused cases per new validation rule is enough, not every possible combination.

---

### Task 1: `MetadataCompiler.validate` + `MetadataValidationError`

**Files:**
- Create: `src/core/metadata/metadata-compiler.ts`
- Create: `src/core/metadata/metadata-compiler.test.ts`
- Modify: `src/core/metadata/metadata-registry.ts`

**Interfaces:**
- Produces: `MetadataValidationError extends Error` (`entity: string`, `issues: string[]`), `MetadataCompiler.validate(entity: EntityDefinition): void` (throws on failure).
- Consumes: `EntityDefinition`/`EntityField`/`EntityListView`/`EntityWorkflow` (`src/core/metadata/entity.ts`, unchanged).

- [ ] **Step 1: Write `MetadataValidationError` and `validate` in `src/core/metadata/metadata-compiler.ts`**

```ts
import type { EntityDefinition } from "./entity";

export class MetadataValidationError extends Error {
  constructor(
    public readonly entity: string,
    public readonly issues: readonly string[],
  ) {
    super(`Invalid metadata for entity "${entity}": ${issues.join("; ")}`);
    this.name = "MetadataValidationError";
  }
}

export const MetadataCompiler = {
  validate(entity: EntityDefinition): void {
    const issues: string[] = [];
    const fieldNames = new Set<string>();

    for (const field of entity.fields) {
      if (fieldNames.has(field.name)) {
        issues.push(`duplicate field name "${field.name}"`);
      }
      fieldNames.add(field.name);

      if (field.kind === "enum" && (!field.enumValues || field.enumValues.length === 0)) {
        issues.push(`field "${field.name}" is kind "enum" but declares no enumValues`);
      }

      if (field.kind === "reference" && !field.refEntity) {
        issues.push(`field "${field.name}" is kind "reference" but declares no refEntity`);
      }
    }

    for (const listView of entity.listViews) {
      for (const fieldName of listView.fields) {
        if (!fieldNames.has(fieldName)) {
          issues.push(`listView "${listView.name}" references unknown field "${fieldName}"`);
        }
      }
      for (const fieldName of listView.filters) {
        if (!fieldNames.has(fieldName)) {
          issues.push(`listView "${listView.name}" filters on unknown field "${fieldName}"`);
        }
      }
      if (listView.defaultSort) {
        const sortField = listView.defaultSort.replace(/^-/, "");
        if (!fieldNames.has(sortField)) {
          issues.push(`listView "${listView.name}" defaultSort references unknown field "${sortField}"`);
        }
      }
    }

    if (entity.workflow) {
      const { stateField, initialState, terminalStates, transitions } = entity.workflow;

      if (!fieldNames.has(stateField)) {
        issues.push(`workflow.stateField "${stateField}" is not a declared field`);
      }
      if (!initialState) {
        issues.push("workflow.initialState must be a non-empty string");
      }
      for (const state of terminalStates) {
        if (!state) {
          issues.push("workflow.terminalStates contains an empty string");
        }
      }

      const seenTransitionKeys = new Set<string>();
      for (const transition of transitions) {
        if (!transition.from || !transition.to || !transition.action) {
          issues.push(
            `workflow transition is missing from/to/action: ${JSON.stringify({
              from: transition.from,
              to: transition.to,
              action: transition.action,
            })}`,
          );
          continue;
        }
        const key = `${transition.from}::${transition.action}`;
        if (seenTransitionKeys.has(key)) {
          issues.push(
            `duplicate transition action "${transition.action}" from state "${transition.from}"`,
          );
        }
        seenTransitionKeys.add(key);
      }
    }

    if (issues.length > 0) {
      throw new MetadataValidationError(entity.name, issues);
    }
  },
};
```

- [ ] **Step 2: Wire into `MetadataRegistry.register()` in `src/core/metadata/metadata-registry.ts`**

```ts
import type { EntityDefinition } from "./entity";
import { MetadataCompiler } from "./metadata-compiler";

export class MetadataRegistry {
  private readonly entities = new Map<string, EntityDefinition>();

  register(entity: EntityDefinition) {
    if (this.entities.has(entity.name)) {
      throw new Error(`Entity already registered: ${entity.name}`);
    }

    MetadataCompiler.validate(entity);
    this.entities.set(entity.name, entity);
  }

  // ... getEntity/getEntityMetadata/listEntities/toMetadata unchanged for this step
}
```

- [ ] **Step 3: Tests in `src/core/metadata/metadata-compiler.test.ts`**

Minimal targeted cases, not a full matrix — build a valid baseline `EntityDefinition` fixture (reuse the shape of `customerEntity` but inline, don't import the real module) and one test per rule:
- Passes on a valid entity (no throw).
- Throws `MetadataValidationError` for: a listView field not in `fields`, an enum field with no `enumValues`, a workflow `stateField` not in `fields`, two transitions sharing the same `(from, action)`.
- `error.issues` contains a message mentioning the offending name (don't assert exact wording, just a substring match) so the test doesn't lock in prose.

- [ ] **Step 4: Verify**

Run: `pnpm typecheck && pnpm vitest run src/core/metadata/metadata-compiler.test.ts`
Expected: no TypeScript errors, all new tests pass.

Run: `pnpm test`
Expected: all existing tests still pass — `customerEntity` must pass validation unchanged (it's already coherent, but this is the first real check of that fact).

- [ ] **Step 5: Leave uncommitted**

---

### Task 2: Cross-entity `refEntity` validation

**Files:**
- Modify: `src/core/metadata/metadata-registry.ts`
- Modify: `src/core/metadata/metadata-registry.test.ts` (create if it doesn't exist yet)
- Modify: `src/core/container.ts`

**Interfaces:**
- Produces: `MetadataRegistry.validateReferences(): void` — throws `MetadataValidationError` if any registered entity's `refEntity` names an entity that was never registered.
- Consumes: `MetadataValidationError` (Task 1).

No entity in this codebase currently declares a `kind: "reference"` field, so this path isn't exercised end-to-end today — write the test with an inline fixture, not `customerEntity`.

- [ ] **Step 1: Add `validateReferences()` to `MetadataRegistry`**

```ts
validateReferences(): void {
  for (const entity of this.entities.values()) {
    const issues: string[] = [];
    for (const field of entity.fields) {
      if (field.kind === "reference" && field.refEntity && !this.entities.has(field.refEntity)) {
        issues.push(
          `field "${field.name}" references unknown entity "${field.refEntity}"`,
        );
      }
    }
    if (issues.length > 0) {
      throw new MetadataValidationError(entity.name, issues);
    }
  }
}
```

- [ ] **Step 2: Call it once from `createContainer`, after every `register()` call**

In `src/core/container.ts`, immediately after `metadata.register(customerEntity)` (and any future `register()` calls, all of which must run first):

```ts
metadata.register(customerEntity);
metadata.validateReferences();
```

- [ ] **Step 3: Test**

One test: two inline fixture entities, one with a `reference` field pointing at an entity name that's never registered — `validateReferences()` throws. One test: pointing at an entity that *is* registered — no throw.

- [ ] **Step 4: Verify**

Run: `pnpm typecheck && pnpm test`
Expected: clean, all pass.

- [ ] **Step 5: Leave uncommitted**

---

### Task 3: `MetadataCompiler.hash` + `version` on the wire

**Files:**
- Modify: `src/core/metadata/metadata-compiler.ts`
- Modify: `src/core/metadata/metadata-registry.ts`
- Modify: `web/src/platform/metadata/types.ts`
- Modify: `src/core/metadata/metadata-compiler.test.ts`

**Interfaces:**
- Produces: `MetadataCompiler.hash(entity: EntityDefinition): string` (SHA-256 hex digest).
- Modifies: `MetadataRegistry.toMetadata()`'s return shape gains `version: string`; `EntitySummary` (frontend) gains optional `version?: string`.

- [ ] **Step 1: Add a `stableStringify` helper and `hash` to `metadata-compiler.ts`**

```ts
import { createHash } from "node:crypto";

function stableStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(",")}]`;
  }
  if (value !== null && typeof value === "object") {
    const keys = Object.keys(value as Record<string, unknown>).sort();
    return `{${keys
      .map((key) => `${JSON.stringify(key)}:${stableStringify((value as Record<string, unknown>)[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}
```

Add `hash` alongside `validate` on the `MetadataCompiler` object:

```ts
hash(entity: EntityDefinition): string {
  const shape = {
    label: entity.label,
    fields: entity.fields,
    listViews: entity.listViews,
    workflow: entity.workflow
      ? {
          stateField: entity.workflow.stateField,
          initialState: entity.workflow.initialState,
          terminalStates: entity.workflow.terminalStates,
          transitions: entity.workflow.transitions.map((t) => ({
            action: t.action,
            from: t.from,
            to: t.to,
            label: t.label,
            // guard functions are intentionally excluded — unrepresentable, already stripped on the wire
          })),
        }
      : undefined,
  };
  return createHash("sha256").update(stableStringify(shape)).digest("hex");
},
```

- [ ] **Step 2: Add `version` in `MetadataRegistry.toMetadata()`**

```ts
private toMetadata(entity: EntityDefinition) {
  return {
    name: entity.name,
    label: entity.label,
    fields: entity.fields,
    listViews: entity.listViews,
    workflow: entity.workflow,
    version: MetadataCompiler.hash(entity),
  };
}
```

- [ ] **Step 3: Add `version?: string` to `EntitySummary` in `web/src/platform/metadata/types.ts`**

- [ ] **Step 4: Tests**

- `hash` is deterministic: same entity fixture hashed twice yields the same string.
- `hash` differs when a field's `label` changes (proves it's not hashing something coarser than intended).
- `hash` is unaffected by workflow transition `guard` presence/absence (since it's excluded) — construct two otherwise-identical fixtures, one with a `guard` function and one without, assert equal hashes.

- [ ] **Step 5: Verify**

Run: `pnpm typecheck && pnpm test` (backend), `cd web && pnpm build` (frontend).
Expected: clean.

- [ ] **Step 6: Leave uncommitted**

---

### Task 4: `metadata_versions` table + migration

**Files:**
- Modify: `src/infra/db/schema.ts`
- Generated: new migration under `src/infra/db/migrations/` + `meta/_journal.json` + new snapshot

**Interfaces:**
- Produces: `metadataVersions` Drizzle table export from `schema.ts`.

- [ ] **Step 1: Add the table to `schema.ts`**

```ts
export const metadataVersions = pgTable("metadata_versions", {
  entityName: varchar("entity_name", { length: 120 }).primaryKey(),
  hash: varchar("hash", { length: 64 }).notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
});
```

No `tenantId` — see Global Constraints. No index beyond the primary key; lookups are always by `entityName`, and this table has one row per entity (dozens at most, not per-tenant).

- [ ] **Step 2: Generate and inspect the migration**

Run: `pnpm db:generate`
Read the generated SQL file before trusting it — confirm it only adds `metadata_versions` and touches nothing else.

- [ ] **Step 3: Migrate both databases**

Confirmed: test files don't auto-migrate `metap_test` in a `beforeAll` — they assume it's already up to date. Run both: `pnpm db:migrate` (dev DB) and `pnpm db:migrate:test` (test DB, per `package.json`'s existing script) before running any tests that touch `metadata_versions`.

- [ ] **Step 4: Verify**

Run: `pnpm typecheck`
Expected: clean (no test yet depends on this table — that's Task 5).

- [ ] **Step 5: Leave uncommitted**

---

### Task 5: Compatibility check wired into `buildApp`

**Files:**
- Create: `src/core/metadata/metadata-drift.ts`
- Modify: `src/server/app.ts`
- Modify: `src/server/app.test.ts`

**Interfaces:**
- Produces: `checkMetadataDrift(db: Database, entities: EntitySummary[], logger: FastifyBaseLogger): Promise<void>` — reads/upserts `metadata_versions`, logs via the passed logger, never throws.
- Consumes: `metadataVersions` (Task 4), `container.metadata.listEntities()` (already returns `version` per Task 3), `container.db`.

- [ ] **Step 1: Write `src/core/metadata/metadata-drift.ts`**

```ts
import { eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { metadataVersions } from "../../infra/db/schema";

export async function checkMetadataDrift(
  db: Database,
  entities: readonly { name: string; version: string }[],
  log: { warn: (obj: unknown, msg: string) => void },
): Promise<void> {
  for (const entity of entities) {
    const [existing] = await db.client
      .select()
      .from(metadataVersions)
      .where(eq(metadataVersions.entityName, entity.name));

    if (!existing) {
      log.warn({ entity: entity.name, hash: entity.version }, "metadata: first boot, recording initial hash");
    } else if (existing.hash !== entity.version) {
      log.warn(
        { entity: entity.name, oldHash: existing.hash, newHash: entity.version },
        "metadata: drift detected since last boot",
      );
    }

    await db.client
      .insert(metadataVersions)
      .values({ entityName: entity.name, hash: entity.version })
      .onConflictDoUpdate({
        target: metadataVersions.entityName,
        set: { hash: entity.version, updatedAt: new Date() },
      });
  }
}
```

- [ ] **Step 2: Call it from `buildApp`, right after `createContainer`**

In `src/server/app.ts`:

```ts
const container = createContainer(config);
await checkMetadataDrift(container.db, container.metadata.listEntities(), app.log);
```

- [ ] **Step 3: Test in `app.test.ts`**

One test: boot `buildApp` twice against the same DB with the same entity metadata (unchanged `customerEntity`) — second boot logs "first boot" only once total across both, not "drift detected" (nothing changed). This may need a way to spy on `app.log.warn` — check how other tests in this file capture/assert on logging, if any; if none, a minimal approach is acceptable (e.g. temporarily lowering the logger level and asserting no thrown error, treating "doesn't crash boot and doesn't leave a stale row" as the practical assertion, per this project's minimal-test-scope preference) rather than building new logging-capture infrastructure for one test.

- [ ] **Step 4: Verify**

Run: `pnpm typecheck && pnpm test`
Expected: clean. Also manually: `pnpm dev` (background), confirm a log line mentioning `metadata:` appears for `crm.customers` on first run against a fresh `metadata_versions` table, then stop the server.

- [ ] **Step 5: Leave uncommitted**

---

### Task 6: OpenAPI generator + `GET /metadata/openapi.json`

**Files:**
- Create: `src/core/metadata/openapi-generator.ts`
- Create: `src/core/metadata/openapi-generator.test.ts`
- Modify: `src/server/routes/metadata.ts`

**Interfaces:**
- Produces: `generateOpenApiDocument(entities: readonly EntitySummary[]): OpenApiDocument`.
- Consumes: `container.metadata.listEntities()`.

- [ ] **Step 1: Write `src/core/metadata/openapi-generator.ts`**

Include a minimal hand-rolled `OpenApiDocument` type (just the shape this generator emits — `openapi`, `info`, `paths`) and a `FIELD_KIND_TO_JSON_SCHEMA` table covering every `FieldKind` from `entity.ts` (`id`→`string`, `string`→`string`, `number`→`number`, `boolean`→`boolean`, `date`→`{type: "string", format: "date"}`, `datetime`→`{type: "string", format: "date-time"}`, `money`→`number`, `enum`→`{type: "string", enum: [...]}` built per-field from `enumValues`, `reference`→`string`, `json`→`{}` (unconstrained)). For each entity, build a JSON Schema object from `fields` and generate path items for `/api/{entityName}` (get, post), `/api/{entityName}/{id}` (patch), and, only if `entity.workflow` is present, `/api/{entityName}/{id}/transitions/{action}` (post) — don't emit a transitions path for entities without a workflow, since that route 404s for them today (check `src/server/routes/records.ts` to confirm this guard is accurate before assuming it).

- [ ] **Step 2: Add the route in `src/server/routes/metadata.ts`**

```ts
app.get("/metadata/openapi.json", async () => generateOpenApiDocument(container.metadata.listEntities()));
```

- [ ] **Step 3: Tests**

- Generates a document with `openapi: "3.1.0"` and one path group per registered entity.
- `crm.customers`' generated schema includes a `status` property with the exact `enum: ["draft", "active", "blocked"]` from the entity's `enumValues` (proves the enum mapping works end-to-end, not just type-level).
- An entity with no `workflow` (build a minimal inline fixture) produces no `/transitions/{action}` path.

- [ ] **Step 4: Verify**

Run: `pnpm typecheck && pnpm vitest run src/core/metadata/openapi-generator.test.ts`
Expected: clean, tests pass.

Manual: `pnpm dev` (background) + mint a token + `curl -H "Authorization: Bearer $TOKEN" http://localhost:3000/metadata/openapi.json | jq .` — confirm valid JSON with a `paths["/api/crm.customers"]` entry. Stop the server.

- [ ] **Step 5: Leave uncommitted**

---

### Task 7: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Full suite, clean**

Run: `pnpm typecheck`
Expected: zero errors, across both the new files and everything they touch (`metadata-registry.ts`, `container.ts`, `app.ts`, `metadata.ts`, frontend `types.ts`).

Run: `pnpm lint`
Expected: no new errors beyond this repo's existing pre-existing baseline (check against the baseline recorded after the 2026-08-01 dependency-audit plan's Task 8 — 18 known errors, none in files this plan touches).

Run: `pnpm test`
Expected: all tests pass, entirely against `metap_test`.

- [ ] **Step 2: Re-confirm `customerEntity` still boots clean**

Run: `pnpm dev` (background), `curl http://localhost:3000/health` → expect `{"status":"ok","checks":{"database":true}}`. Check the server log for the expected `metadata:` line for `crm.customers`. Stop the server.

- [ ] **Step 3: Deliberately break something to prove validation actually fires**

Temporarily edit `src/modules/crm/customer.entity.ts` — e.g. add a `filters: ["nonexistent"]` entry to the `default` listView — run `pnpm dev` and confirm the process crashes at boot with a `MetadataValidationError` mentioning `"nonexistent"`, not a silent success or a runtime crash on first request. Revert the edit immediately after confirming (this file must not be left modified — it's not part of this plan's intended diff).

- [ ] **Step 4: Manual OpenAPI smoke test**

Per Task 6 Step 4's manual check, if not already run this session.

---

## Plan Self-Review Notes

- **Spec coverage:** Goal 1 (startup validation) → Tasks 1-2. Goal 2 (version hash) → Task 3. Goal 3 (drift detection) → Tasks 4-5. Goal 4 (OpenAPI) → Task 6.
- **Deliberately deferred to the spec's Non-goals**, not silently dropped from this plan: auto-deriving `EntityField` from the Zod `schema`, blocking boot on drift, and any UI changes.
- **Task 4/5 risk flagged explicitly**: this is the first plan to add a migration since the 2026-08-01 dependency/test-DB work; Task 4 Step 3 calls out needing to check the established test-DB migration pattern rather than assuming one.
