-- General-purpose audit trail for CrudService's create/update/delete/transition writes
-- (`../../metap-docs/docs/audits/05-crud-audit-trail-gap.md`) — deliberately separate from
-- `workflow_events` (that table stays exactly as-is, a narrow ledger for the workflow engine's
-- own state transitions, not a business audit trail). `metadata` schema already exists as of
-- 0028_metadata_schema.sql, so this table is created directly schema-qualified rather than the
-- older create-then-`ALTER ... SET SCHEMA` two-step 0001/0013 used.
--
-- One shared table, not per-entity dedicated tables — matches every other framework-level
-- append-only log in this codebase (workflow_events, outbox_events,
-- low_code_metadata_audit_events are all single shared tables too).
--
-- No down-migration, no retention/pruning column or index — this table growing forever is a
-- compliance requirement, not a leak, by explicit project-owner direction.
CREATE TABLE metadata.audit_trail_entries (
    "id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
    "tenant_id" uuid NOT NULL,
    "entity" varchar(200) NOT NULL,
    "record_id" uuid NOT NULL,
    "action" varchar(20) NOT NULL,
    "transition_action" varchar(80),
    "actor_user_id" uuid,
    "reason" text,
    "diff" jsonb NOT NULL DEFAULT '{}'::jsonb,
    "version_after" integer,
    "occurred_at" timestamp with time zone DEFAULT now() NOT NULL
);

CREATE INDEX "audit_trail_entries_tenant_entity_record_idx" ON metadata.audit_trail_entries USING btree ("tenant_id", "entity", "record_id", "occurred_at");
