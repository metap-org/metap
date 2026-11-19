-- Login sessions (roadmap 115 M4): one row per sign-in, so a person can see where they are signed
-- in and end one session without ending the rest. A session token carries the row's id as "sid";
-- a revoked or missing row ends that token at once, whatever its exp. Tokens without a "sid" (API
-- keys, OAuth2 access tokens, service tokens) are not sessions and are unaffected.
CREATE TABLE metadata.sessions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  identity_id  uuid NOT NULL REFERENCES metadata.identities (id),
  member_id    uuid NOT NULL,
  tenant_id    uuid NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  ip           varchar(64),
  user_agent   varchar(300),
  revoked_at   timestamptz
);
CREATE INDEX sessions_identity_idx ON metadata.sessions (identity_id, created_at DESC);
CREATE INDEX sessions_expiry_idx ON metadata.sessions (expires_at);
