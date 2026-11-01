-- OIDC-authenticated users have no local password — JIT-provisioned on first successful OIDC
-- login (Bước 3/3, docs/roadmap.md's tenant-auth phase), identified by the IdP's stable `sub`
-- claim (external_subject), not email (email can change at the IdP; sub does not).
ALTER TABLE users
  ALTER COLUMN password_hash DROP NOT NULL,
  ADD COLUMN auth_provider text NOT NULL DEFAULT 'local',
  ADD COLUMN external_subject text;

ALTER TABLE users
  ADD CONSTRAINT users_local_requires_password_hash
  CHECK (auth_provider <> 'local' OR password_hash IS NOT NULL);

-- Lets a repeat OIDC login find the same local user row it JIT-provisioned last time, instead of
-- creating a duplicate — also the constraint that makes a provisioning race (two concurrent first
-- logins for the same IdP identity) fail loudly instead of silently creating two rows.
CREATE UNIQUE INDEX users_tenant_external_subject_idx
  ON users (tenant_id, auth_provider, external_subject)
  WHERE external_subject IS NOT NULL;
