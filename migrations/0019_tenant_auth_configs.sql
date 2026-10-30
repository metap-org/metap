-- A tenant can have multiple auth providers enabled at once (e.g. local password AND OIDC SSO
-- side by side) — not a single exclusive strategy, hence one row per (tenant, provider_kind)
-- rather than a column on control.tenants.
CREATE TABLE tenant_auth_configs (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  provider_kind text NOT NULL,
  enabled      boolean NOT NULL DEFAULT true,
  config       jsonb NOT NULL DEFAULT '{}',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider_kind)
);

CREATE INDEX tenant_auth_configs_tenant_id_idx ON tenant_auth_configs (tenant_id) WHERE enabled;

-- Backfill: every tenant that existed before this migration keeps working exactly as before
-- (local email+password, the only auth path that has ever existed) without needing a code-level
-- fallback for "tenant has zero rows here yet".
INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config)
SELECT id, 'local', true, '{}'::jsonb FROM control.tenants
ON CONFLICT (tenant_id, provider_kind) DO NOTHING;
