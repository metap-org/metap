-- Tenant credentials stored in metap's own database, encrypted (the "database" mode of the
-- workspace Credentials tab). `ref` is control.TenantSecretRef (derived, never supplied); the
-- plaintext is sealed with AES-256-GCM under an application key that is NOT in the database
-- (SECRETS_ENCRYPTION_KEYS), with `ref` as the authenticated data so a ciphertext cannot be
-- replayed under another tenant's or key's reference. `key_id` names the key that sealed it, so
-- keys can be rotated: new writes use the active key, old rows stay readable until re-saved.
CREATE TABLE metadata.tenant_secrets (
  ref        text PRIMARY KEY,
  ciphertext bytea NOT NULL,
  nonce      bytea NOT NULL,
  key_id     text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
