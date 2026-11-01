-- Corrects 0028: `low_code_entity_drafts`/`low_code_entity_versions`/
-- `low_code_metadata_audit_events`/`lowcode_impersonation_sessions` were put in `control`
-- alongside `control.tenants`/`tenant_hostnames`, but `control` carries a stronger meaning than
-- "metap-lowcode's own tables" — `metap-control::provision_dedicated_db_tenant` unconditionally
-- drops the whole `control` schema from every `dedicated_db` tenant's own database right after
-- migrating it, because `control.tenants` is genuinely global platform data that has no business
-- existing per-tenant. Low-code entity definitions are NOT global — a `dedicated_db` tenant needs
-- its own `low_code_entity_versions` row in its own database (found live via
-- `run_tick_reaches_a_dedicated_db_tenant_own_database`, `reconciler-orchestrator`'s own e2e
-- test, immediately after 0028 first ran). `metadata` is not dropped from a dedicated tenant's
-- database, so that's where these belong instead.
ALTER TABLE control.low_code_entity_drafts SET SCHEMA metadata;
ALTER TABLE control.low_code_entity_versions SET SCHEMA metadata;
ALTER TABLE control.low_code_metadata_audit_events SET SCHEMA metadata;
ALTER TABLE control.lowcode_impersonation_sessions SET SCHEMA metadata;
