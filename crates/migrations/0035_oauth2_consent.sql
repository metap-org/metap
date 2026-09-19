-- Real interactive consent for `GET /oauth/authorize`, closing the gap
-- `../metap-docs/docs/roadmap/88-oauth2-authorization-server.md` flagged as future frontend work:
-- before this migration, any already-authenticated caller silently "approved" any registered
-- client's requested scope with no screen shown at all.
--
-- Two tables, same `metadata` schema and same "not tenant-schema-split" reasoning as
-- `0034_oauth2.sql`'s three (a lookup keyed by a caller-supplied client_id/pending-request id
-- carries no tenant hint of its own):
--
-- `oauth_pending_authorizations` — one row per in-flight `GET /oauth/authorize` that actually
-- needs a screen (i.e. no matching prior consent). Short-lived (10 minutes, enforced in
-- application code, same shape as `oauth_authorization_codes`'s 60s) — created when the consent
-- page is shown, consumed (deleted) the moment the user approves or denies. Holds the *entire*
-- original request (redirect_uri/scope/PKCE challenge) so the approve/deny POST never has to
-- re-trust caller-supplied query params a second time — it only ever trusts what this row
-- already validated when it was created.
--
-- `oauth_consents` — one row per (client, user) that has ever approved, so a returning user
-- isn't nagged on every single authorization round trip (the standard "you've already granted
-- this app access" IdP behavior). `scope` is the *union* of everything ever approved for that
-- pair; a later request for a narrower or equal scope skips the screen, a request for something
-- new re-prompts (and the approval then widens this row rather than replacing it).
CREATE TABLE metadata.oauth_pending_authorizations (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id             uuid NOT NULL REFERENCES metadata.oauth_clients (id),
  tenant_id             uuid NOT NULL,
  user_id               uuid NOT NULL,
  redirect_uri          text NOT NULL,
  scope                 text NOT NULL DEFAULT '',
  code_challenge        text NULL,
  code_challenge_method text NULL,
  client_state          text NULL,
  expires_at            timestamptz NOT NULL,
  created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE metadata.oauth_consents (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id    uuid NOT NULL REFERENCES metadata.oauth_clients (id),
  tenant_id    uuid NOT NULL,
  user_id      uuid NOT NULL,
  scope        text NOT NULL DEFAULT '',
  granted_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (client_id, user_id)
);
