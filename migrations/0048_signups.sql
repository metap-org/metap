-- Self-service signup (roadmap 115 M2): a pending signup is created by POST /auth/signup and
-- becomes a tenant only when its emailed link is opened, which proves the address. The chosen
-- password is kept only as an argon2id hash.
CREATE TABLE metadata.signups (
  token_hash    bytea PRIMARY KEY,
  email         varchar(320) NOT NULL,
  tenant_name   varchar(200) NOT NULL,
  display_name  varchar(200),
  password_hash text NOT NULL,
  expires_at    timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX signups_email_idx ON metadata.signups (lower(email), created_at);
