-- Identity vs. membership (roadmap 113, P1). A person (identity: email + password) can belong to
-- several tenants; metadata.users becomes the membership — one row per (tenant, identity) — and
-- keeps its id, so the JWT `sub`, user_roles.user_id, audit actors and every created_by stay valid.
-- Both tables live in the shared database only: login finds an identity before it knows a tenant.

CREATE TABLE metadata.identities (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email               varchar(320) NOT NULL,
  -- NULL for an identity that only ever signs in through a tenant's external IdP.
  password_hash       text,
  -- 'disabled' blocks sign-in everywhere (platform admin only); a tenant admin disables the
  -- membership instead.
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  failed_logins       integer NOT NULL DEFAULT 0,
  locked_until        timestamptz,
  -- Every token issued before this instant is rejected (password change/reset, disable).
  sessions_revoked_at timestamptz,
  last_login_at       timestamptz,
  last_tenant_id      uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX identities_email_unique ON metadata.identities (lower(email));

ALTER TABLE metadata.users
  ADD COLUMN identity_id         uuid REFERENCES metadata.identities (id),
  ADD COLUMN display_name        varchar(200),
  ADD COLUMN status              text NOT NULL DEFAULT 'active'
                                 CHECK (status IN ('invited', 'active', 'disabled')),
  ADD COLUMN sessions_revoked_at timestamptz,
  ADD COLUMN last_login_at       timestamptz,
  ADD COLUMN invited_by          uuid,
  ADD COLUMN deleted_at          timestamptz;

-- One identity per distinct email (emails were globally unique already, lower() only guards
-- against a case-only duplicate), keeping the oldest row's password.
INSERT INTO metadata.identities (email, password_hash, created_at)
SELECT DISTINCT ON (lower(email)) email, password_hash, created_at
FROM metadata.users
ORDER BY lower(email), created_at;

UPDATE metadata.users u
SET identity_id = i.id
FROM metadata.identities i
WHERE lower(i.email) = lower(u.email);

ALTER TABLE metadata.users ALTER COLUMN identity_id SET NOT NULL;

-- The credential now lives on the identity only: a second copy here could only go stale.
ALTER TABLE metadata.users DROP CONSTRAINT IF EXISTS users_local_requires_password_hash;
ALTER TABLE metadata.users DROP COLUMN password_hash;

-- email stays on the membership as a copy of the identity's (read by pickers and lists without
-- a join); uniqueness moves to (tenant, identity).
DROP INDEX IF EXISTS metadata.users_email_unique;
CREATE UNIQUE INDEX users_tenant_identity_unique
  ON metadata.users (tenant_id, identity_id) WHERE deleted_at IS NULL;
CREATE INDEX users_identity_idx ON metadata.users (identity_id);

-- Shown in the tenant picker; filled in by platform admins (roadmap 113 P3), NULL meanwhile.
ALTER TABLE control.tenants ADD COLUMN name varchar(200);
