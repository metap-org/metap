-- Tracking for `metap-lowcode`'s platform-admin tenant-switcher
-- (`../metap-lowcode/docs/features/01-platform-admin-tenant-switcher.md`) — physically lives
-- here (not a migration inside `metap-lowcode`) for the same reason the `low_code_*` tables
-- already do: the whole platform shares one Postgres schema and one `db-migrate` binary, both in
-- this repo, regardless of which downstream crate actually owns the feature.
--
-- Deliberately its own table, not a new column on `user_roles` — this only records *who looked
-- at which tenant when*, for the reaper to find expired grants and for basic audit. It is never
-- consulted by `metap_http::auth::AuthContext`'s live role lookup (`SELECT role FROM user_roles
-- WHERE tenant_id = $1 AND user_id = $2`, unchanged) — the actual authorization grant is a real
-- `user_roles` row, written via the existing `metap_peripherals::assign_role`, so a stolen token
-- is still only as powerful as a real DB row backing it, same invariant every other session in
-- this platform already relies on.
--
-- Lives in the shared/platform pool (`AppState.pool`), never tenant-routed — same category as
-- `control.tenant_hostnames`: a fact about platform-admin behavior, not a target tenant's own
-- business data, so it must never end up inside a `DedicatedDb` tenant's own physical database.
CREATE TABLE lowcode_impersonation_sessions (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id     uuid NOT NULL,
  target_tenant_id  uuid NOT NULL,
  granted_at        timestamptz NOT NULL DEFAULT now(),
  expires_at        timestamptz NOT NULL,
  revoked_at        timestamptz
);

-- What the reaper scans: every still-active grant, ordered by nothing in particular since it
-- just needs "which ones are past due" cheaply.
CREATE INDEX lowcode_impersonation_sessions_active_idx
  ON lowcode_impersonation_sessions (target_tenant_id, expires_at)
  WHERE revoked_at IS NULL;
