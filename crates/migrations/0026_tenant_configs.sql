-- Per-tenant config overrides, the third tier of `docs/features/18-config-tiers-db-backed.md`
-- (slice 2). The resolution chain a read walks is:
--
--   default declared in Rust  <-  platform_configs (fleet default)  <-  tenant_configs (this table)
--
-- Only keys `metap_config::keys::REGISTRY` declares as `Tenant` may ever have a row here; a key at
-- any other tier is rejected by the write path, not by a constraint, because the tier is a property
-- of the key declared in Rust and deliberately never a column (see that module's doc comment).
--
-- Tenant-scoped like `dashboard_configs`/`tenant_auth_configs`, and reached the same way — through
-- `Router::begin(tenant)`, so a `DedicatedDb` tenant's overrides live in that tenant's own
-- database. The `tenant_id` column still exists because a `Schema`-strategy tenant shares `public`
-- with every other one.
CREATE TABLE tenant_configs (
  tenant_id  uuid NOT NULL,
  key        text NOT NULL,
  value      jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

-- Which hostnames a tenant answers on. Lives in `control` (the platform-wide schema, alongside
-- `control.tenants`) rather than next to `tenant_configs` above, because it is read *before* any
-- tenant is known: the unauthenticated theme endpoint has nothing but a `Host` header to go on, so
-- the lookup cannot itself be tenant-routed.
--
-- Written only by an operator (`dev-tools set-tenant-hostname`), never by a tenant admin: a tenant
-- that could claim an arbitrary hostname could claim another tenant's, and serve its own branding
-- on that tenant's login screen.
CREATE TABLE control.tenant_hostnames (
  hostname   text PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES control.tenants (id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tenant_hostnames_tenant_id_idx ON control.tenant_hostnames (tenant_id);
