-- Orchestrator fan-out (docs/multi-tenant-platform-design.md §6, docs/features/04-table-per-entity.md
-- step 4): `desired_version > applied_version` is the work queue and the global resume source —
-- an orchestrator process dying mid-rollout loses nothing, a restart just re-queries this table.
-- `public` schema, matching `reconciler_entity_status`/`reconciler_backfill_progress`
-- (crates/migrations/0017_reconciler_tables.sql) rather than the design doc's illustrative
-- `control.entity_deployments` name — this crate has never used a dedicated schema.

CREATE TABLE reconciler_entity_deployments (
	tenant_id uuid NOT NULL,
	entity_name varchar(200) NOT NULL,
	desired_version bigint NOT NULL DEFAULT 0,
	applied_version bigint,
	status varchar(20) NOT NULL DEFAULT 'pending', -- pending | running | done | failed | blocked
	failure_class varchar(20), -- transient | data_error | fatal — set alongside 'failed'/'blocked'
	attempts integer NOT NULL DEFAULT 0,
	last_error text,
	priority_tier integer NOT NULL DEFAULT 0,
	lease_worker text,
	lease_heartbeat timestamp with time zone,
	updated_at timestamp with time zone DEFAULT now() NOT NULL,
	PRIMARY KEY (tenant_id, entity_name)
);

CREATE INDEX reconciler_entity_deployments_due_idx ON reconciler_entity_deployments (entity_name, status)
	WHERE status IN ('pending', 'failed');
