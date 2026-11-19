-- Tenant lifecycle (roadmap 113, P3): a tenant that asked to be deleted is suspended at once and
-- purged after a retention window, during which the request can be cancelled. Only the two
-- timestamps are stored; the tenant's status (suspended, later deleted) stays the single source
-- of truth for what Router does.
ALTER TABLE control.tenants
  ADD COLUMN deletion_requested_at timestamptz,
  ADD COLUMN deletion_effective_at timestamptz;

CREATE INDEX tenants_deletion_due_idx ON control.tenants (deletion_effective_at)
  WHERE deletion_effective_at IS NOT NULL AND status <> 'deleted';
