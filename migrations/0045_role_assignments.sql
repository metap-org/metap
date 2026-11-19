-- Roles held inside a scope (roadmap 115, M1). user_roles still holds the tenant-wide roles; a row
-- here gives a user a role only on the records of one scope: "analyst on zone Z", "viewer on
-- account A". Which entity records belong to which scope is declared by the entity's `scopes`
-- metadata, so this table is generic: scope_type is a name, scope_id the id of the scope record.
--
-- Next to user_roles (shared metadata, tenant_id column; a dedicated_db tenant's copy sits in its
-- own database the same way).
CREATE TABLE metadata.role_assignments (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  user_id    uuid NOT NULL,
  role       varchar(80) NOT NULL,
  scope_type varchar(80) NOT NULL CHECK (scope_type ~ '^[a-z][a-z0-9_]*$'),
  scope_id   varchar(200) NOT NULL CHECK (scope_id <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid
);
CREATE UNIQUE INDEX role_assignments_unique
  ON metadata.role_assignments (tenant_id, user_id, role, scope_type, scope_id);
-- "Who has access to this zone?"
CREATE INDEX role_assignments_scope_idx ON metadata.role_assignments (tenant_id, scope_type, scope_id);
