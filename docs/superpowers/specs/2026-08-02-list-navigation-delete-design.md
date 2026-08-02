# List Navigation + Record Delete

Date: 2026-08-02

Status: approved

Scope: a follow-up sub-project to Phase 6 (Frontend Core), discovered during manual verification of the phase's original 5 sub-projects — `GeneratedList` had no way to actually reach `GeneratedForm`'s create route, `RecordDetail`, or delete a record, even though the routes themselves (`/new`, `/:id`, `/:id/edit`) already existed with nothing linking to them. Delete didn't exist anywhere (frontend or backend) before this.

Related specs: `docs/superpowers/specs/2026-08-02-{field-renderer,generated-form,workflow-action-bar,permission-aware-ui,list-pagination}-design.md` (the 5 sub-projects this one follows).

## Motivation

Verifying the finished Phase 6 work surfaced two gaps:

1. Navigating straight to `/records/:entity/new` by typing the URL is a full page reload, which drops the in-memory-only auth token (deliberate, documented behavior — see `CLAUDE.md`) and redirects to `/dev-login`. That's expected, not a bug — but it exposed that there was no *in-app* way to reach that route at all, since `GeneratedList` links to nothing.
2. There is no delete anywhere — not in the UI, and not in the backend (`deleted` exists as a soft-delete column on `records`, filtered everywhere reads happen, but no route or `CrudService` method ever sets it to `true`).

## Design

### Backend: soft-delete

**`EntityAction`** (`src/core/permission/permission-service.ts`) extends from `"read" | "create" | "update"` to `"read" | "create" | "update" | "delete"`. No DB migration: the `policies.action` column is a free-text `varchar`, not a constrained enum.

**`PermissionService.canDeleteEntity(context, entity)`**, mirroring the existing `canCreateEntity`/`canUpdateEntity` exactly (same shape, same default-allow-everything scaffold behavior described in `CLAUDE.md`).

**`CrudService.delete(entityName, id, expectedVersion, context)`**:
1. Look up the entity; 404 if unknown.
2. `canDeleteEntity` — 403 if denied.
3. Load the existing row (tenant-scoped, `deleted: false`); 404 if not found.
4. `snapshot.canUpdateRecordCondition(context, existingData, "delete")` — a record-level check under the `"delete"` action, deliberately distinct from `"update"`'s record-level policies (the same pattern `get()` already established by checking `"read"` separately from `update()`'s `"update"` check) — 403 if denied.
5. `UPDATE records SET deleted = true, version = version + 1, updated_at = now(), updated_by = ? WHERE id = ? AND tenant_id = ? AND entity = ? AND version = ? AND deleted = false RETURNING *` — optimistic-locked exactly like `update()`; no matching row (stale version or already deleted) → `409 version_conflict`, same as `update()`.
6. On success: `WorkflowEngine.emitDeleted()` (new method, mirrors `emitCreated`/`emitUpdated`, topic `<entity>.record.deleted`, payload `{ recordId }`), inside the same transaction as the update, then return `{ ok: true, data: this.maskRecordForRead(...) }`.

**Route**: `DELETE /api/:entity/:id`, `{ version: number }` body (consistent with `PATCH`'s convention — every mutating endpoint in this API takes `version` in the body, not a query param or header). Returns `{ data: <soft-deleted record> }`, 200.

### Frontend: list navigation + delete

**`GeneratedList`**:
- A "New" link/button above the table, routing to `/records/:entityName/new`.
- A new action column (last column, not part of `listView.fields`/not virtualized differently from other cells — it's just another `<Table.Td>` per row) with a "View" link (→ `/records/:entityName/:id`) and a "Delete" button.
- Delete: `window.confirm("Delete this record?")` guard, then a direct `apiFetch("DELETE", ...)` call using `useAuth()`'s token — not a `useApiMutation` hook, for the same reason `WorkflowActionBar`'s transition buttons don't use one: the path is per-row (`/api/:entity/:id`), and a mutation hook bound to one fixed path at the top of a component can't serve a dynamic per-row target from inside a `.map()` (hooks can't be called conditionally/in a loop). On success, calls the infinite query's own `refetch()` (already returned by `useApiInfiniteQuery`) to refresh the visible list. On failure, shows the error inline (a small text/alert near the row, or a page-level alert — implementation detail, not a design fork).

**`RecordDetail`**: a Delete button next to the existing Edit link, same confirm+direct-`apiFetch`-DELETE pattern; on success, navigates back to `/records/:entityName`.

## Out of scope (deliberate, not an oversight)

- **No proactive per-row delete-capability signal on the list.** Matches sub-project 4's (Permission-aware UI) established boundary — `list()` doesn't compute per-row `capabilities`, only `get()` does. A list-row Delete button attempts the delete and shows the real error reactively, same as every other list-row action would.
- **No restore/undelete UI.** The `deleted` column supports it structurally, but nothing in this sub-project exposes a way to view or restore soft-deleted records.
- **No delete gating on workflow state.** Whether a record can be deleted is orthogonal to its workflow state (e.g. a terminal-state record isn't specially protected) — only permission decides.

## Testing

Backend (`canDeleteEntity`, `CrudService.delete()`) follows this project's TDD convention — minimal, targeted, live-DB integration tests in the existing `crud-service.test.ts` pattern:
- Deletes a record when the version matches; the record then 404s on a subsequent `get()`.
- Rejects a delete with a stale version (`409 version_conflict`), record remains un-deleted.
- Rejects a delete blocked by a record-level `"delete"` policy (403), distinct from an `"update"` policy that would otherwise allow it (proves the two actions are checked separately, not conflated).

Frontend: same as sub-projects 1-5 — `web/` has no test framework, verification is `tsc -b`/`pnpm build` + `oxlint` + manual browser check. This sandbox has been unable to run a headless browser for every sub-project this session (missing system libraries, no `sudo`, no cached alternative); if still true, that gets reported plainly rather than claiming visual verification succeeded — this time, unlike prior sub-projects, the user has already started doing that verification manually themselves and found the underlying gap this spec addresses.
