-- API keys: long-lived bearer credentials for a member (usually a service account), roadmap 115 M5.
-- A key authenticates AS its member, so it carries exactly that member's roles; it is stored only
-- as a SHA-256 hash and shown once at creation. Shared across tenants (like users): the request
-- carries just the token, so the tenant is found from the key row.
CREATE TABLE metadata.api_keys (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  member_id    uuid NOT NULL REFERENCES metadata.users (id),
  name         varchar(200) NOT NULL,
  prefix       varchar(16) NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  created_by   uuid,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_member_idx ON metadata.api_keys (tenant_id, member_id);
