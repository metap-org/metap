-- Tenant owner (roadmap 115 M8): one member per tenant who is more than an admin. Other admins
-- cannot disable, remove, demote or reset the owner; ownership moves only by the owner's own
-- transfer, or by a platform-admin recovery that tenant admins are told about and can cancel
-- during a 72-hour wait. The owner is assigned lazily (the oldest active admin) the first time
-- anything asks, so tenants that existed before this migration need no backfill.
ALTER TABLE metadata.users ADD COLUMN is_owner boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX users_one_owner_per_tenant ON metadata.users (tenant_id) WHERE is_owner AND deleted_at IS NULL;

CREATE TABLE metadata.owner_recoveries (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  new_owner_member   uuid NOT NULL,
  requested_by       uuid NOT NULL,
  requested_by_email text NOT NULL,
  reason             text NOT NULL,
  effective_at       timestamptz NOT NULL,
  cancelled_at       timestamptz,
  cancelled_by       uuid,
  completed_at       timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);
-- At most one recovery in flight per tenant.
CREATE UNIQUE INDEX owner_recoveries_one_pending ON metadata.owner_recoveries (tenant_id)
  WHERE cancelled_at IS NULL AND completed_at IS NULL;
