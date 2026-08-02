# Generate Frontend Metadata Types from Backend OpenAPI

Date: 2026-08-02

Status: approved

Scope: fifth sub-project of the DB/architecture review batch (sub-projects 1-4: `docs/superpowers/specs/2026-08-02-{outbox-row-locking,permission-storage-seam,outbox-per-service-db,db-boot-visibility}-design.md`). Different concern (frontend/backend contract drift, not DB coupling) but part of the same "let's fix real gaps found while reviewing the architecture" session.

## Motivation

`packages/platform-react/src/metadata/types.ts` hand-declares `EntityField`/`EntityWorkflow`/`EntitySummary`/etc. as a manual mirror of `packages/core/src/core/metadata/entity.ts`'s real types — nothing keeps them in sync; a backend field addition silently doesn't show up in frontend types until someone remembers to update them by hand. Frontend can't import backend TypeScript directly (`apps/demo -.HTTP only.-> packages/core`'s routes, per `05-building-blocks.md`), so the fix has to derive frontend types from the wire contract itself, not from backend source.

Investigating this surfaced that `GET /metadata/entities`/`GET /metadata/entities/{entity}` (what `useEntity`/`useEntities` actually call, and where this drift risk lives) aren't documented in the existing OpenAPI generator at all — it only covers `/api/{entity}*`'s per-entity **business data** shape, a different concern. This sub-project extends it.

## Design

### Backend: a wire-contract schema for the meta-model

New file `packages/core/src/core/metadata/entity-wire-schema.ts` — Zod schemas describing exactly what crosses the wire, not backend's internal types:

```ts
import { z } from "zod";

export const FieldKindSchema = z.enum([
  "id", "string", "number", "boolean", "date", "datetime", "money", "enum", "reference", "json",
]);

export const EntityFieldSchema = z.object({
  name: z.string(),
  label: z.string(),
  kind: FieldKindSchema,
  required: z.boolean().optional(),
  indexed: z.boolean().optional(),
  unique: z.boolean().optional(),
  enumValues: z.array(z.string()).optional(),
  refEntity: z.string().optional(),
  searchable: z.boolean().optional(),
  searchMode: z.enum(["substring", "fts"]).optional(),
  sortable: z.boolean().optional(),
});

export const EntityListViewSchema = z.object({
  name: z.string(),
  label: z.string(),
  fields: z.array(z.string()),
  filters: z.array(z.string()),
  defaultSort: z.string().optional(),
  maxLimit: z.number(),
});

// No `guard` — it's a server-side function, stripped by JSON.stringify,
// never actually present on the wire. This schema describes reality, not
// backend's in-memory WorkflowTransition type.
export const WorkflowTransitionSchema = z.object({
  action: z.string(),
  from: z.string(),
  to: z.string(),
  label: z.string(),
});

export const EntityWorkflowSchema = z.object({
  stateField: z.string(),
  initialState: z.string(),
  terminalStates: z.array(z.string()),
  transitions: z.array(WorkflowTransitionSchema),
});

export const EntitySummarySchema = z.object({
  name: z.string(),
  label: z.string(),
  fields: z.array(EntityFieldSchema),
  listViews: z.array(EntityListViewSchema),
  workflow: EntityWorkflowSchema.optional(),
  version: z.string(),
});
```

`generateOpenApiDocument` (`openapi-generator.ts`) gains:
- `components: { schemas: { EntitySummary: z.toJSONSchema(EntitySummarySchema, { target: "draft-7" }) } }` — one self-contained, nested schema (matches this codebase's already-established `z.toJSONSchema` usage in every route's request validation — not the unused `zod-to-json-schema` npm dependency, which this sub-project also removes from `package.json` as dead weight).
- Two new paths: `GET /metadata/entities` (response: array of `$ref EntitySummary`) and `GET /metadata/entities/{entity}` (response: `$ref EntitySummary`).

**Nested types derived by TS indexing, not separate `$ref` components.** `EntityField`/`EntityWorkflow`/etc. don't need their own `components.schemas` entries — they're nested inside `EntitySummary`'s single schema, and the generated frontend type gets indexed into (see below) to produce clean, separately-named types. Avoids any Zod-registry/multi-schema-`$ref` complexity for a payoff (separately browsable OpenAPI component docs) nothing here actually needs.

### Frontend: `openapi-typescript` + a thin façade

`packages/platform-react` gets `openapi-typescript` as a devDependency and a script:

```json
"generate:types": "openapi-typescript http://localhost:3000/metadata/openapi.json -o src/metadata/generated-types.ts"
```

Run manually (dev server must be running) whenever the meta-model changes; **the output is committed to git** like any other source file — reviewable in diffs, no build-time network/tooling dependency added to `pnpm build`/`pnpm dev`.

`metadata/types.ts` becomes a thin façade over the generated file, keeping every existing export name and shape the rest of the codebase already imports (`FieldValue`, `FieldInput`, `GeneratedForm`, `GeneratedList`, `WorkflowActionBar`, `RecordDetail`, `useEntity`, `useEntities` all keep importing from `./types` completely unchanged):

```ts
import type { components } from "./generated-types";

export type EntitySummary = components["schemas"]["EntitySummary"];
export type EntityField = EntitySummary["fields"][number];
export type EntityListView = EntitySummary["listViews"][number];
export type EntityWorkflow = NonNullable<EntitySummary["workflow"]>;
export type WorkflowTransition = EntityWorkflow["transitions"][number];
export type FieldKind = EntityField["kind"];
```

Every consuming file's imports and usage stay identical — this is a pure "where do these types come from" change, not a shape change (the generated shape matches today's hand-written shape field-for-field, by design).

## Testing

- Backend: extend `openapi-generator.test.ts` — assert `doc.components.schemas.EntitySummary` exists with the expected top-level properties, and that `GET /metadata/entities`/`GET /metadata/entities/{entity}` paths exist and reference it.
- Frontend: no test framework (established boundary) — verification is `pnpm typecheck`/`pnpm build` across `packages/platform-react` and `apps/demo` (proves every existing consumer still compiles against the generated types with zero changes) + `pnpm lint`.
- The generation step itself (`openapi-typescript` run) isn't something to "test" — it's a one-shot codegen action; its output is verified indirectly by everything downstream typechecking correctly.

## Out of scope

- Generating types for anything beyond the entity metadata contract (`/api/{entity}*`'s business-data shapes, admin routes, etc.) — narrowly scoped to the actual drift risk that was raised.
- Automatic/build-time regeneration — explicitly rejected in favor of a committed, manually-regenerated file (see Design).
- Separate `$ref` component schemas for `EntityField`/`EntityWorkflow`/etc. — TS-side indexing into the one `EntitySummary` schema is simpler and sufficient.
