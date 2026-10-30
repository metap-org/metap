CREATE SCHEMA IF NOT EXISTS control;

CREATE TABLE control.tenants (
  id               uuid PRIMARY KEY,
  tier             text NOT NULL,
  strategy         text NOT NULL,
  schema_name      text,
  dsn_secret_ref   text,
  status           text NOT NULL,
  trial_expires_at timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);
