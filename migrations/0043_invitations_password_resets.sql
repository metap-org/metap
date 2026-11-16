-- Invitations and password resets (roadmap 113, P2). Both live in the shared database next to
-- identities and memberships: accepting an invitation or resetting a password happens before any
-- tenant session exists. Only a SHA-256 of each token is stored; the token itself exists only in
-- the email (and, for an invitation the mail relay didn't take, in the inviting admin's response).

CREATE TABLE metadata.invitations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  -- The member row the invitation activates (status 'invited' until accepted).
  member_id    uuid NOT NULL REFERENCES metadata.users (id),
  identity_id  uuid NOT NULL REFERENCES metadata.identities (id),
  -- True when the identity was created for this invitation (no password, no other tenant): only
  -- then may accepting it set the password.
  new_identity boolean NOT NULL,
  token_hash   bytea NOT NULL,
  invited_by   uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  accepted_at  timestamptz,
  revoked_at   timestamptz
);
CREATE UNIQUE INDEX invitations_token_hash_unique ON metadata.invitations (token_hash);
CREATE INDEX invitations_member_idx ON metadata.invitations (member_id);

CREATE TABLE metadata.password_resets (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  identity_id uuid NOT NULL REFERENCES metadata.identities (id),
  token_hash  bytea NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz
);
CREATE UNIQUE INDEX password_resets_token_hash_unique ON metadata.password_resets (token_hash);
CREATE INDEX password_resets_identity_created_idx ON metadata.password_resets (identity_id, created_at);
