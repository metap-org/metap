-- Platform-level attachment metadata (metap-attachments's first/default table) — a plain table
-- like `policies`/`cron_jobs`, not a metadata-driven EntityDefinition, since `record_id` can
-- point at a row in ANY entity's table (the generic `records` table or any dedicated
-- table-per-entity table), which rules out a typed foreign key the way a normal `Reference`
-- field gets. An entity expecting heavy attachment volume can opt into its own dedicated table
-- instead (same 8-column shape, `metap_attachments::ensure_dedicated_table`) — this is only the
-- shared default every tenant DB always has.
CREATE TABLE attachments (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  entity_name  text NOT NULL,
  record_id    uuid NOT NULL,
  filename     text NOT NULL,
  key          text NOT NULL,
  size         bigint NOT NULL,
  content_type text,
  created_by   uuid,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_tenant_entity_record_idx ON attachments (tenant_id, entity_name, record_id);
