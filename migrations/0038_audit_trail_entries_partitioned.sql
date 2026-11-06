-- Partition metadata.audit_trail_entries by month (occurred_at).
--
-- Why: the table is append-only and, by design, never pruned (compliance), and one shared table
-- serves every tenant of every product. At WAAP's design scale that is ~300M rows/year, and an
-- unpartitioned table of that size makes every maintenance task (vacuum, index rebuild, moving
-- old history to cheaper storage) a whole-table operation. Partitioning changes none of the
-- semantics: no row is deleted here or by any job this migration adds. An operator who must keep
-- history forever but not in the primary database can now DETACH an old partition and archive it
-- as a unit, instead of running a multi-hour DELETE.
--
-- Layout:
--   * metadata.audit_trail_entries        — partitioned by RANGE (occurred_at)
--   * metadata.audit_trail_entries_YYYYMM  — one partition per calendar month (UTC)
--   * metadata.audit_trail_entries_default — catches anything outside the created months, so an
--     insert never fails because a partition is missing; metadata.ensure_audit_partitions moves
--     such rows into their month partition when it creates it.
--
-- The primary key must include the partition key on a partitioned table, so it becomes
-- (id, occurred_at). Nothing in the codebase looks a row up by id alone.

ALTER TABLE metadata.audit_trail_entries RENAME TO audit_trail_entries_unpartitioned;
ALTER INDEX metadata.audit_trail_entries_tenant_entity_record_idx RENAME TO audit_trail_entries_unpartitioned_ter_idx;
ALTER TABLE metadata.audit_trail_entries_unpartitioned RENAME CONSTRAINT audit_trail_entries_pkey TO audit_trail_entries_unpartitioned_pkey;

CREATE TABLE metadata.audit_trail_entries (
    "id" uuid DEFAULT gen_random_uuid() NOT NULL,
    "tenant_id" uuid NOT NULL,
    "entity" varchar(200) NOT NULL,
    "record_id" uuid NOT NULL,
    "action" varchar(20) NOT NULL,
    "transition_action" varchar(80),
    "actor_user_id" uuid,
    "reason" text,
    "diff" jsonb NOT NULL DEFAULT '{}'::jsonb,
    "version_after" integer,
    "occurred_at" timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY ("id", "occurred_at")
) PARTITION BY RANGE ("occurred_at");

CREATE INDEX "audit_trail_entries_tenant_entity_record_idx"
    ON metadata.audit_trail_entries USING btree ("tenant_id", "entity", "record_id", "occurred_at");

CREATE TABLE metadata.audit_trail_entries_default PARTITION OF metadata.audit_trail_entries DEFAULT;

-- ensure_audit_partitions(from_month, months_ahead): makes sure a partition exists for every
-- month from from_month (truncated to the month, UTC) through months_ahead months after the
-- current month. A month whose rows already landed in the default partition gets them moved in
-- before the partition is attached. Idempotent; safe to run on a schedule.
CREATE OR REPLACE FUNCTION metadata.ensure_audit_partitions(from_month date, months_ahead integer)
RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
    m date := date_trunc('month', from_month)::date;
    last_month date := (date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => months_ahead))::date;
    part text;
    created integer := 0;
BEGIN
    WHILE m <= last_month LOOP
        part := 'audit_trail_entries_' || to_char(m, 'YYYYMM');
        IF to_regclass('metadata.' || part) IS NULL THEN
            EXECUTE format('CREATE TABLE metadata.%I (LIKE metadata.audit_trail_entries INCLUDING DEFAULTS)', part);
            EXECUTE format(
                'WITH moved AS (DELETE FROM metadata.audit_trail_entries_default
                   WHERE occurred_at >= %L::timestamptz AND occurred_at < %L::timestamptz RETURNING *)
                 INSERT INTO metadata.%I SELECT * FROM moved',
                m::timestamp AT TIME ZONE 'UTC', (m + interval '1 month')::timestamp AT TIME ZONE 'UTC', part);
            EXECUTE format(
                'ALTER TABLE metadata.audit_trail_entries ATTACH PARTITION metadata.%I
                   FOR VALUES FROM (%L) TO (%L)',
                part, m::timestamp AT TIME ZONE 'UTC', (m + interval '1 month')::timestamp AT TIME ZONE 'UTC');
            created := created + 1;
        END IF;
        m := (m + interval '1 month')::date;
    END LOOP;
    RETURN created;
END;
$$;

-- Partitions for every month that already has history, plus the next 12 months, then copy the
-- existing rows across (they route to their month partitions on insert).
SELECT metadata.ensure_audit_partitions(
    COALESCE((SELECT min(occurred_at) FROM metadata.audit_trail_entries_unpartitioned), now())::date,
    12);

INSERT INTO metadata.audit_trail_entries
    ("id", "tenant_id", "entity", "record_id", "action", "transition_action", "actor_user_id",
     "reason", "diff", "version_after", "occurred_at")
SELECT "id", "tenant_id", "entity", "record_id", "action", "transition_action", "actor_user_id",
       "reason", "diff", "version_after", "occurred_at"
FROM metadata.audit_trail_entries_unpartitioned;

DROP TABLE metadata.audit_trail_entries_unpartitioned;
