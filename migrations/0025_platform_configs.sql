-- Fleet-wide platform settings that used to be literals in Rust source
-- (`docs/features/18-config-tiers-db-backed.md` slice 1; audit 04 A#7 for the GraphQL pair
-- specifically). Written only through `PUT /platform/config` by a `platform_admin`, and only for
-- keys `metap_config::keys::REGISTRY` declares as `PlatformGlobal`.
--
-- No `tenant_id` column, deliberately: this table is the *global* tier. Per-tenant overrides get
-- their own `tenant_configs` table in slice 2, so that the two tiers can never be confused for one
-- another by a query that forgot a WHERE clause.
--
-- Starts empty. Every key has a default declared in Rust, so an empty table means "behave exactly
-- as the hard-coded values did" — this migration changes no behavior on its own.
CREATE TABLE platform_configs (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
