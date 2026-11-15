-- Record which record owns each audited record (EntityAuditConfig.Parent, e.g. a firewall rule's
-- zoneId), so a parent's history can include its children's entries in one paged query instead
-- of one request per child — and keeps including a child's history after the child is deleted.
--
-- Nullable: entities that declare no parent, and rows written before this migration (backfill
-- them with `dev-tools backfill-audit-parents`). Added on the partitioned parent table, so every
-- existing month partition and the default partition get the columns, and
-- metadata.ensure_audit_partitions' `LIKE metadata.audit_trail_entries` copies them into new
-- partitions.
ALTER TABLE metadata.audit_trail_entries
    ADD COLUMN "parent_entity" varchar(200),
    ADD COLUMN "parent_id" uuid;

CREATE INDEX "audit_trail_entries_tenant_parent_idx"
    ON metadata.audit_trail_entries USING btree ("tenant_id", "parent_entity", "parent_id", "occurred_at");
