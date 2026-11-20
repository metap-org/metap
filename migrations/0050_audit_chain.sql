-- Tamper-evident audit trail (roadmap 115 M9): every entry of a tenant carries its position in
-- that tenant's chain (seq, from 1) and a hash that covers the previous entry's hash, so editing,
-- deleting, inserting or reordering a row breaks every later link and `audit.VerifyChain` finds
-- it. Rows written before this migration have seq/hash NULL and are simply outside the chain.
--
-- The head table is the per-tenant serialisation point: a writer locks its tenant's head row
-- (SELECT ... FOR UPDATE), reads the last hash, inserts the entry and moves the head, in one
-- transaction. Writers of different tenants never wait for each other. The head also tells the
-- verifier how long the chain should be, so cutting the tail off is detected too.
ALTER TABLE metadata.audit_trail_entries
    ADD COLUMN "seq" bigint,
    ADD COLUMN "prev_hash" bytea,
    ADD COLUMN "hash" bytea;

CREATE INDEX "audit_trail_entries_tenant_seq_idx"
    ON metadata.audit_trail_entries USING btree ("tenant_id", "seq");

-- The tenant-wide search (who did what, when) filters by tenant and time, newest first.
CREATE INDEX "audit_trail_entries_tenant_time_idx"
    ON metadata.audit_trail_entries USING btree ("tenant_id", "occurred_at" DESC, "id" DESC);

CREATE TABLE metadata.audit_chain_heads (
    "tenant_id" uuid PRIMARY KEY,
    "seq" bigint NOT NULL,
    "hash" bytea NOT NULL
);

-- The one definition of an entry's hash, used by the writer and by the verifier so they cannot
-- drift. jsonb::text is canonical (sorted keys, normalised numbers), timestamps are fixed to UTC
-- microseconds (what the column stores), fields are separated by the unit separator so
-- ("ab","c") and ("a","bc") differ.
CREATE OR REPLACE FUNCTION metadata.audit_entry_hash(
    prev bytea, seq bigint, tenant uuid, id uuid, entity varchar, record uuid, action varchar,
    transition varchar, actor uuid, reason text, diff jsonb, version integer, occurred timestamptz,
    parent_entity varchar, parent_id uuid)
RETURNS bytea LANGUAGE sql IMMUTABLE AS $$
    SELECT sha256(
        coalesce(prev, '\x00'::bytea) ||
        convert_to(concat_ws(E'\x1f',
            seq::text, tenant::text, id::text, entity, record::text, action,
            coalesce(transition, ''), coalesce(actor::text, ''), coalesce(reason, ''),
            diff::text, coalesce(version::text, ''),
            to_char(occurred AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'),
            coalesce(parent_entity, ''), coalesce(parent_id::text, '')), 'UTF8'))
$$;
