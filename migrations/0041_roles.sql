-- Roles as data (roadmap 114, P2). Until now a tenant's role catalog was derived: the built-in
-- admin plus every role some policy named. A role now has its own row — label, description,
-- creatable empty, deletable once nothing uses it.
--
-- Lives next to user_roles (shared metadata, tenant_id column; a dedicated_db tenant's copy sits in
-- its own database the same way), not cloned per tenant schema like policies: an unqualified
-- `roles` resolves through Router's search_path ({schema}, metadata, control) either way.

CREATE TABLE metadata.roles (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  -- The key user_roles.role and policies.roles hold; never renamed.
  name        varchar(80) NOT NULL,
  label       varchar(200),
  description text,
  -- Defined by the platform (admin, platform_admin): not deletable.
  builtin     boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid,
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX roles_tenant_name_unique ON metadata.roles (tenant_id, name);

-- Backfill: every role a member holds, and the built-in admin of every tenant known here.
INSERT INTO metadata.roles (tenant_id, name, builtin)
SELECT DISTINCT tenant_id, role, role IN ('admin', 'platform_admin')
FROM metadata.user_roles
ON CONFLICT (tenant_id, name) DO NOTHING;

DO $$
BEGIN
  IF to_regclass('control.tenants') IS NOT NULL THEN
    INSERT INTO metadata.roles (tenant_id, name, builtin)
    SELECT id, 'admin', true FROM control.tenants
    ON CONFLICT (tenant_id, name) DO NOTHING;
  END IF;
END $$;

-- The platform tenant (the nil uuid) always has both built-ins.
INSERT INTO metadata.roles (tenant_id, name, builtin) VALUES
  ('00000000-0000-0000-0000-000000000000', 'admin', true),
  ('00000000-0000-0000-0000-000000000000', 'platform_admin', true)
ON CONFLICT (tenant_id, name) DO NOTHING;

-- Every role a policy names, from the shared policies table and every tenant schema's own copy.
-- The same pass splits a basic policy row naming several roles into one row per role, the shape
-- the permission matrix (and roadmap 114's per-role editing) writes and edits.
DO $$
DECLARE
  tbl text;
BEGIN
  FOR tbl IN
    SELECT quote_ident(table_schema) || '.policies'
    FROM information_schema.tables
    WHERE table_name = 'policies' AND table_type = 'BASE TABLE'
  LOOP
    EXECUTE format($q$
      INSERT INTO metadata.roles (tenant_id, name, builtin)
      SELECT DISTINCT p.tenant_id, r, r IN ('admin', 'platform_admin')
      FROM %s p, jsonb_array_elements_text(p.roles) AS r
      WHERE jsonb_typeof(p.roles) = 'array'
      ON CONFLICT (tenant_id, name) DO NOTHING
    $q$, tbl);

    EXECUTE format($q$
      WITH multi AS (
        DELETE FROM %1$s
        WHERE jsonb_typeof(roles) = 'array' AND jsonb_array_length(roles) > 1
          AND condition IS NULL AND field IS NULL AND subject = 'context' AND effect = 'allow'
        RETURNING tenant_id, entity, action, roles, created_at, created_by
      )
      INSERT INTO %1$s (tenant_id, entity, action, roles, condition, created_at, created_by, field, subject, effect)
      SELECT m.tenant_id, m.entity, m.action, jsonb_build_array(r), NULL, m.created_at, m.created_by,
             NULL, 'context', 'allow'
      FROM multi m, jsonb_array_elements_text(m.roles) AS r
    $q$, tbl);
  END LOOP;
END $$;
