-- Single sign-on over OIDC (roadmap 115 M7). A tenant's IdP settings live in
-- tenant_auth_configs (provider_kind 'oidc'); this table holds the short-lived state of one
-- sign-in in flight, so the callback can be served by any instance. One row per /auth/sso/start,
-- consumed exactly once by the callback and useless after ten minutes.
CREATE TABLE metadata.sso_states (
  state         text PRIMARY KEY,
  tenant_id     uuid NOT NULL,
  nonce         text NOT NULL,
  pkce_verifier text NOT NULL,
  expires_at    timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sso_states_expiry_idx ON metadata.sso_states (expires_at);
