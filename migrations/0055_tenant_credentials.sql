-- Named credentials a tenant keeps for its integrations (the workspace Credentials tab). This
-- table holds what is NOT secret: the name, kind and where the value lives. The value itself is
-- in the metap database sealed (metadata.tenant_secrets) or in the deployment's secret manager,
-- under control.TenantCredentialRef(tenant_id, id) — derived from the row, never typed by anyone,
-- so a credential can only ever be read back for its own tenant.
CREATE TABLE metadata.tenant_credentials (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  kind        text NOT NULL,
  source      text NOT NULL,
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tenant_credentials_name_idx ON metadata.tenant_credentials (tenant_id, lower(name));
