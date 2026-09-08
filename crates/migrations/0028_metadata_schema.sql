CREATE SCHEMA IF NOT EXISTS metadata;

ALTER TABLE public.users SET SCHEMA metadata;
ALTER TABLE public.user_roles SET SCHEMA metadata;
ALTER TABLE public.user_preferences SET SCHEMA metadata;
ALTER TABLE public.policies SET SCHEMA metadata;
ALTER TABLE public.outbox_events SET SCHEMA metadata;
ALTER TABLE public.workflow_events SET SCHEMA metadata;
ALTER TABLE public.workflow_runs SET SCHEMA metadata;
ALTER TABLE public.cron_jobs SET SCHEMA metadata;
ALTER TABLE public.cron_job_runs SET SCHEMA metadata;
ALTER TABLE public.metadata_versions SET SCHEMA metadata;
ALTER TABLE public.reconciler_backfill_progress SET SCHEMA metadata;
ALTER TABLE public.reconciler_entity_deployments SET SCHEMA metadata;
ALTER TABLE public.reconciler_entity_status SET SCHEMA metadata;
ALTER TABLE public.platform_configs SET SCHEMA metadata;
ALTER TABLE public.tenant_configs SET SCHEMA metadata;
ALTER TABLE public.tenant_auth_configs SET SCHEMA metadata;
ALTER TABLE public.dashboard_configs SET SCHEMA metadata;

ALTER TABLE public.low_code_entity_drafts SET SCHEMA control;
ALTER TABLE public.low_code_entity_versions SET SCHEMA control;
ALTER TABLE public.low_code_metadata_audit_events SET SCHEMA control;
ALTER TABLE public.lowcode_impersonation_sessions SET SCHEMA control;

-- Every unqualified query anywhere in this codebase against a table moved above still needs to
-- resolve without a code change — `current_database()` (not a literal name) so this works
-- identically whether it runs against the shared platform DB or a `dedicated_db` tenant's own
-- database (both get this same migration file, `metap-control::provision_dedicated_db_tenant`).
-- This is the *default* for any connection that doesn't set its own `search_path` (dev-tools,
-- cron-scheduler, outbox-publisher, `Router::pool_for`'s bare shared-pool path, migrations
-- themselves next time they run). `Router::begin`'s own `SET LOCAL search_path` (schema-strategy
-- tenants) replaces the search path per-transaction and must list `metadata` itself too — this
-- default alone does not cover that path, since `SET LOCAL` overrides rather than extends it.
DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I SET search_path TO public, metadata, control', current_database());
END
$$;
