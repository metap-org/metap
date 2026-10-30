-- `docs/features/15-tenant-scoped-lowcode-metadata.md`: low-code entity definitions used to be
-- one global set shared by every tenant (`metap_metadata::MetadataResolver` merged the same
-- `list_enabled_published()` into every tenant's registry, no scoping at all). This migration
-- gives `metadata.low_code_entity_drafts`/`metadata.low_code_entity_versions` a real `tenant_id`
-- column so a tenant's low-code entities are no longer visible to every other tenant.
--
-- Any row that already exists predates this feature and has no real tenant to attribute to — it
-- is attributed to `metap_control::PLATFORM_TENANT_ID` (`00000000-0000-0000-0000-000000000000`,
-- the existing cross-tenant sentinel this codebase already uses for platform-wide identities, see
-- that constant's own doc comment) rather than invented or dropped, so an existing deployment's
-- published entities keep resolving for whichever tenant was relying on the old global-merge
-- behavior. This table lives in the shared `metadata` schema, not per-tenant
-- (`metap_control::tenant_schema::TENANT_SCOPED_TABLES` deliberately excludes it, same reasoning
-- as `users`/`oauth_clients`) — the storage design is "centrally stored, filtered by tenant_id in
-- every query", not "one physical copy per tenant".
ALTER TABLE metadata.low_code_entity_drafts ADD COLUMN "tenant_id" uuid;
UPDATE metadata.low_code_entity_drafts SET "tenant_id" = '00000000-0000-0000-0000-000000000000' WHERE "tenant_id" IS NULL;
ALTER TABLE metadata.low_code_entity_drafts ALTER COLUMN "tenant_id" SET NOT NULL;
ALTER TABLE metadata.low_code_entity_drafts DROP CONSTRAINT low_code_entity_drafts_pkey;
ALTER TABLE metadata.low_code_entity_drafts ADD PRIMARY KEY ("tenant_id", "entity_name");
--> statement-breakpoint

ALTER TABLE metadata.low_code_entity_versions ADD COLUMN "tenant_id" uuid;
UPDATE metadata.low_code_entity_versions SET "tenant_id" = '00000000-0000-0000-0000-000000000000' WHERE "tenant_id" IS NULL;
ALTER TABLE metadata.low_code_entity_versions ALTER COLUMN "tenant_id" SET NOT NULL;
ALTER TABLE metadata.low_code_entity_versions DROP CONSTRAINT low_code_entity_versions_entity_version_unique;
ALTER TABLE metadata.low_code_entity_versions ADD CONSTRAINT low_code_entity_versions_tenant_entity_version_unique
	UNIQUE ("tenant_id", "entity_name", "version_number");
--> statement-breakpoint

DROP INDEX IF EXISTS metadata.low_code_entity_versions_entity_idx;
CREATE INDEX "low_code_entity_versions_tenant_entity_idx" ON metadata.low_code_entity_versions USING btree ("tenant_id", "entity_name");
