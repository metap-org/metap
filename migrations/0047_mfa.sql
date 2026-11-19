-- Multi-factor authentication (roadmap 115 M3): a TOTP factor per identity (people sign in with
-- one account across tenants, so MFA belongs to the identity, not the membership), one-time
-- recovery codes (stored hashed), and short-lived challenges that carry "password was right" from
-- the first login step to the second.
CREATE TABLE metadata.mfa_factors (
  identity_id    uuid PRIMARY KEY REFERENCES metadata.identities (id),
  secret         text NOT NULL,
  confirmed_at   timestamptz,
  last_used_step bigint NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE metadata.mfa_recovery_codes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  identity_id uuid NOT NULL REFERENCES metadata.identities (id),
  code_hash   bytea NOT NULL,
  used_at     timestamptz,
  UNIQUE (identity_id, code_hash)
);

CREATE TABLE metadata.mfa_challenges (
  token_hash  bytea PRIMARY KEY,
  identity_id uuid NOT NULL REFERENCES metadata.identities (id),
  tenant_id   uuid,
  purpose     varchar(16) NOT NULL,
  attempts    int NOT NULL DEFAULT 0,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_expiry_idx ON metadata.mfa_challenges (expires_at);
