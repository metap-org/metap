-- metap as an OAuth2 Authorization Server (RFC 6749) issuing tokens to third-party clients on
-- behalf of a tenant's own user — distinct from `tenant_auth_configs` (how a user logs *into*
-- metap) and from `control.tenants`/`users` (identity itself). All 3 tables are explicitly
-- schema-qualified into `metadata` (`0028_metadata_schema.sql` already created that schema),
-- same as `0032_audit_trail_entries.sql`'s own `CREATE TABLE metadata.audit_trail_entries` —
-- **not** left unqualified: this database's default `search_path` is `public, metadata, control`
-- (`public` first), so an unqualified `CREATE TABLE` here would land in `public`, the opposite of
-- what every other post-0028 platform table does.
--
-- Scope shipped here (`../metap-docs/docs/roadmap/88-oauth2-authorization-server.md` has the full writeup):
-- `authorization_code` (+ PKCE) and `refresh_token` grants only. `client_credentials` is a
-- registered grant type a client can request but the token endpoint rejects with
-- `unsupported_grant_type` today — it needs a service-user identity to mint a token *as*, which
-- this migration doesn't provision; a deliberate v1 cut, not an oversight.
CREATE TABLE metadata.oauth_clients (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL,
  client_id         text NOT NULL,
  -- SHA-256 hex digest, never the raw secret — the secret is generated server-side and shown to
  -- the caller exactly once, at creation (`../metap-docs/docs/roadmap/88-oauth2-authorization-server.md`'s `POST /admin/oauth/clients`).
  -- Fast digest, not argon2: this is a high-entropy, server-generated 256-bit secret, not a
  -- human-chosen password, so the offline-brute-force threat a slow hash defends against doesn't
  -- apply (same reasoning `oauth_refresh_tokens.token_hash` below uses).
  client_secret_hash text NOT NULL,
  name              text NOT NULL,
  redirect_uris     text[] NOT NULL,
  allowed_scopes    text[] NOT NULL DEFAULT '{}',
  -- A public client (mobile/SPA, can't keep a secret confidential) must use PKCE; a confidential
  -- client may still use PKCE, it's just not required. `client_secret_hash` is still populated
  -- either way today (no client-registration flow issues a secretless public client yet) — this
  -- flag only changes whether `POST /oauth/token` demands a `code_verifier`.
  is_confidential   boolean NOT NULL DEFAULT true,
  created_at        timestamptz NOT NULL DEFAULT now(),
  revoked_at        timestamptz NULL,
  UNIQUE (client_id)
);

CREATE INDEX oauth_clients_tenant_id_idx ON metadata.oauth_clients (tenant_id) WHERE revoked_at IS NULL;

-- Short-lived (60s, enforced in application code), single-use. `code_hash` rather than the raw
-- code, matching `client_secret_hash` above and the "never store a raw credential" rule
-- `tenant_secret_ref`/`SecretStore` already follow elsewhere in this platform — a code is a
-- bearer credential for the duration of its (short) life.
CREATE TABLE metadata.oauth_authorization_codes (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code_hash             text NOT NULL,
  client_id             uuid NOT NULL REFERENCES metadata.oauth_clients (id),
  tenant_id             uuid NOT NULL,
  user_id               uuid NOT NULL,
  redirect_uri          text NOT NULL,
  scope                 text NOT NULL DEFAULT '',
  code_challenge        text NULL,
  code_challenge_method text NULL,
  expires_at            timestamptz NOT NULL,
  used_at               timestamptz NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (code_hash)
);

-- Opaque, long-lived (default 30 days — `../metap-docs/docs/roadmap/88-oauth2-authorization-server.md` has the exact default),
-- rotated on every use (`replaced_by`, a linked list forward in time) so a stolen-and-replayed
-- refresh token is detectable: presenting an already-`revoked_at` token is treated as reuse and
-- revokes the rest of that client/user's live chain (`metap_oauth_server::consume_refresh_token`'s
-- own doc comment has the reuse-detection reasoning).
-- Access tokens themselves are stateless JWTs (same trust root `mint_jwt`/`metap-jwks` already
-- use) and are never stored here — the only real "revoke" lever this platform has today is
-- cutting off the refresh token that would mint the *next* one; an already-issued access token
-- is good until its own (short) `exp`, same accepted limitation `mint_jwt`'s own doc comment
-- notes for every other token this platform mints (no revocation-checking infrastructure yet).
CREATE TABLE metadata.oauth_refresh_tokens (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash   text NOT NULL,
  client_id    uuid NOT NULL REFERENCES metadata.oauth_clients (id),
  tenant_id    uuid NOT NULL,
  user_id      uuid NOT NULL,
  scope        text NOT NULL DEFAULT '',
  expires_at   timestamptz NOT NULL,
  revoked_at   timestamptz NULL,
  replaced_by  uuid NULL REFERENCES metadata.oauth_refresh_tokens (id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (token_hash)
);

CREATE INDEX oauth_refresh_tokens_client_user_idx ON metadata.oauth_refresh_tokens (client_id, user_id);

-- Deliberately **not** added to `metap_control::tenant_schema::TENANT_SCOPED_TABLES` despite
-- every table here carrying `tenant_id` — same reasoning that module's own doc comment gives for
-- excluding `metadata.users`/`user_roles` (audit 06 finding #2): `POST /oauth/token` and
-- `POST /oauth/revoke` resolve which tenant a request belongs to *from the client/code/token row
-- they find*, using a caller-supplied `client_id`/code/refresh token that carries no tenant hint
-- of its own — there is no tenant picker on this endpoint, exactly the shape that made cloning
-- `users` break login. Splitting these 3 tables per tenant schema would make that lookup
-- impossible (the row could be in any of N schemas) rather than merely redundant. Left shared,
-- filtered by each row's own `tenant_id` column, resolved via the platform's shared pool —
-- `crates/metap-http/src/routes/oauth2.rs`'s own header names this explicitly.
--
-- No cleanup job ships in this pass — an expired code/refresh-token row is inert (every read path
-- re-checks `expires_at`/`revoked_at`/`used_at`) but not physically deleted. Unlike
-- `metadata.audit_trail_entries`, this is *not* a deliberate "never prune" compliance table, just
-- an unaddressed gap: flagged in the roadmap doc as a follow-up (a periodic `DELETE ... WHERE
-- expires_at < now() - interval`, the same shape `reconciler_backfill_progress` or a cron job
-- could run), not built here.
