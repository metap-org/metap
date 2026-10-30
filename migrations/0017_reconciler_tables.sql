-- Table-per-entity reconciler (docs/multi-tenant-platform-design.md §5, docs/features/04-table-per-entity.md
-- step 2): per-(tenant, entity) reconcile status + a lease/heartbeat for the watchdog (§5.8),
-- and checkpointed progress for batched backfills (§5.7). Entity-agnostic, platform/ops config —
-- same category as `cron_jobs`/`policies`, not an `EntityDefinition`.

CREATE TABLE reconciler_entity_status (
	tenant_id uuid NOT NULL,
	entity_name varchar(200) NOT NULL,
	status varchar(20) NOT NULL DEFAULT 'active', -- active | migrating | error
	lease_owner uuid,
	lease_expires_at timestamp with time zone,
	attempts integer NOT NULL DEFAULT 0,
	last_error text,
	updated_at timestamp with time zone DEFAULT now() NOT NULL,
	PRIMARY KEY (tenant_id, entity_name)
);

CREATE TABLE reconciler_backfill_progress (
	tenant_id uuid NOT NULL,
	entity_name varchar(200) NOT NULL,
	op_id varchar(200) NOT NULL,
	cursor_id uuid,
	completed boolean NOT NULL DEFAULT false,
	updated_at timestamp with time zone DEFAULT now() NOT NULL,
	PRIMARY KEY (tenant_id, entity_name, op_id)
);
