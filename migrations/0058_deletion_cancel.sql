-- Cancelling a workspace deletion by link (UC-81). When an owner asks to delete the workspace it is
-- suspended for the retention period, which also ends their own sessions, so cancelling cannot go
-- through the admin area. The owner and every admin get an emailed link instead; only a SHA-256 of
-- the token is stored, and it is good until the deletion would take effect.
CREATE TABLE metadata.deletion_cancel_tokens (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  token_hash bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  used_at    timestamptz
);
CREATE UNIQUE INDEX deletion_cancel_tokens_hash_unique ON metadata.deletion_cancel_tokens (token_hash);
CREATE INDEX deletion_cancel_tokens_tenant_idx ON metadata.deletion_cancel_tokens (tenant_id);
