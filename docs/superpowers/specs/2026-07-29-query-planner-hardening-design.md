# QueryPlanner Hardening: Metadata-Constrained Filters + Real Sort

Date: 2026-07-29

Status: approved

Scope: third of four planned Phase 1 kernel pieces, in this priority order:

1. Auth + RequestContext + structured errors + request/trace id (done)
2. `CrudService` update + optimistic locking (done)
3. **`QueryPlanner` hardening** (this spec)
4. `PermissionService` (real RBAC/ABAC)

## Motivation

`QueryPlanner.planList` (`src/core/query/query-planner.ts`) currently only enforces tenant scope and a max limit. It accepts no filters from the client at all, and `input.sort` is accepted by the API but silently ignored — every list is always `ORDER BY created_at DESC`. This is a gap, not a regression: the capability was never built. `docs/architecture.md`'s stated rule — "filter/sort fields must come from entity metadata (never arbitrary client-supplied operators)" — has nothing to violate yet because there's no filtering at all.

The legacy system audited earlier in this project (see prior review, not referenced by name per this project's licensing constraints) had exactly the opposite failure mode: it *did* accept client-supplied filters, but forwarded them into MongoDB queries with no operator allowlist, letting a client-controlled object value (e.g. `?field[$ne]=null`) become a live NoSQL-operator-injection surface. This design builds the missing filter/sort capability the safe way from the start, rather than adding a permissive version now and hardening it later.

## Design

### Request shape

`GET /api/:entity?status=active&name=Acme&sort=-name`

- `limit`, `sort` are the only reserved/known querystring keys (`cursor` is removed — see below).
- Every other querystring key is a *candidate* filter. The route layer does the bare minimum: drop `limit`/`sort`, and drop any value that isn't a plain string (Fastify/qs turns a repeated key like `?status=a&status=b` into an array — arrays and objects are discarded outright, not passed through). This alone closes the "object-shaped value" injection class the legacy system had.
- The candidate filters (`Record<string, string>`) are handed to `CrudService.list` → `QueryPlanner.planList`, which does the actual allowlist check against entity metadata. The allowlist check lives in `QueryPlanner`, not the route, because `QueryPlanner` is the architectural boundary CLAUDE.md already designates as "the only place list/filter/sort queries are turned into SQL" — the route has no business deciding what's filterable.

### `QueryPlanner.planList` behavior

For each candidate filter key:
- If the key is **not** in `entity.listViews[0].filters`, it's silently ignored (not an error — an unknown/mistyped filter key just doesn't filter anything, matching how extra querystring params are already handled elsewhere in this API).
- If the key **is** allowed: look up the matching `EntityField`. If `field.searchable === true`, use a case-insensitive **contains** match (`ILIKE '%value%'`). Otherwise, use exact **equality**. No other operators exist — the client cannot request a different comparison.
- The field's value lives inside the `data` JSONB column for every field except the system columns `createdAt`/`updatedAt`. Access it via `jsonb_extract_path_text(data, fieldName)`, with `fieldName` passed as a genuine bound SQL parameter through Drizzle's `sql` tagged template (not string-concatenated) — so even though `fieldName` only ever comes from the server-side allowlist (never directly from client input), the construction itself carries no injection risk regardless of where the string originated.

For sort:
- Allowed sort fields are `{ f.name for f in entity.fields where f.sortable === true } ∪ { "createdAt", "updatedAt" }` (the two system timestamp columns are always sortable and always real top-level columns, never inside `data`).
- `input.sort` (e.g. `"-name"` or `"name"`) is parsed: a leading `-` means descending. If the resulting field name isn't in the allowed set, fall back to `entity.listViews[0].defaultSort`, and if that's also unusable, fall back to `"-createdAt"`.
- `createdAt`/`updatedAt` sort directly on the real column; every other allowed sort field uses the same `jsonb_extract_path_text` expression as filtering.

### Removing `cursor`

`ListInput.cursor` and `ListQuerySchema`'s `cursor` field are deleted outright. They currently do nothing — accepting a parameter that's silently a no-op is worse than not accepting it, since it implies capability that doesn't exist. Real keyset/cursor pagination is a separate, later concern (`docs/roadmap.md` Phase 4, "Add keyset pagination") and gets reintroduced there when it's actually built.

## Out of scope (deliberate, not an oversight)

- **No type-aware filter coercion.** Every filter comparison is a text comparison via `jsonb_extract_path_text`, which is correct today because every filterable field on the one entity that exists (`crm.customers`) is a string/enum. Numeric/date/boolean-aware filtering is deferred until an entity actually needs it.
- **No optimization of `code`/`status` to use their mirrored top-level columns instead of `jsonb_extract_path_text`.** Both are duplicated into real columns today (for indexing), but this pass is about safety, not performance — `docs/roadmap.md` Phase 2 ("Add generated column/index strategy for hot JSONB fields") is where that optimization belongs.
- **No real cursor/keyset pagination.** Roadmap Phase 4, separate work.
- **No RBAC/field-level permission on filters.** `PermissionService` stays a stub — item 4 in the priority list above.

## Testing (minimal — important cases only)

- One test: filtering by an allowed equality field (`status`) returns only matching rows.
- One test: filtering by an allowed searchable field (`name`) with a partial value matches via contains.
- One test: an unrecognized filter key (not in the entity's `listViews[0].filters`) is silently ignored — the list is unaffected, not an error.
- One test: sorting by an explicit allowed field works in both directions (`name` vs `-name`).
- One test: an invalid/unsortable `sort` value falls back to the entity's default sort rather than erroring.

No exhaustive matrix beyond that, per project convention.
