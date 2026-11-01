-- Customizable dashboard layouts (`metap-dashboards`) — a plain ops table like `cron_jobs`/
-- `policies`, not a metadata-driven `EntityDefinition`: a layout is platform/UI config, not
-- business data, same category as those.
--
-- `owner_user_id IS NULL` is the tenant-wide default layout (one per tenant, admin-write —
-- enforced at the HTTP layer, not by a DB constraint); a non-null `owner_user_id` is that one
-- user's personal override. The effective dashboard for a request is: that user's own row if it
-- exists, else the tenant default row, else nothing (the caller falls back to a hardcoded
-- default widget set) — see `metap_dashboards::get_effective_dashboard`.
CREATE TABLE dashboard_configs (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL,
  owner_user_id  uuid,
  layout         jsonb NOT NULL,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);

-- One tenant-default row and one personal row per user — `NULLS NOT DISTINCT` so two
-- `owner_user_id IS NULL` rows for the same tenant collide too (Postgres treats NULLs as
-- distinct by default, which would otherwise let multiple "tenant default" rows pile up).
CREATE UNIQUE INDEX dashboard_configs_tenant_owner_idx
  ON dashboard_configs (tenant_id, owner_user_id) NULLS NOT DISTINCT;
