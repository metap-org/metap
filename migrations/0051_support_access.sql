-- Support access (roadmap 115 M10): a tenant admin lets the vendor's support staff act inside the
-- tenant for a limited time, with a stated reason, and can end it at once.
--
-- A grant is the tenant's permission (reason, the role support will hold, expiry). Each time a
-- platform admin starts a session under a grant, a service-account member "Support: <email>" is
-- created in the tenant with exactly that role and an API key that expires with the grant — so
-- everything support does is attributed to that member in the audit trail, shows in the tenant's
-- service-account list, and stops when the key expires or the grant is revoked.
CREATE TABLE metadata.support_grants (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  granted_by  uuid NOT NULL,
  reason      text NOT NULL,
  role        varchar(100) NOT NULL,
  expires_at  timestamptz NOT NULL,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_grants_tenant_idx ON metadata.support_grants (tenant_id, created_at DESC);

CREATE TABLE metadata.support_sessions (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  grant_id         uuid NOT NULL REFERENCES metadata.support_grants (id),
  tenant_id        uuid NOT NULL,
  member_id        uuid NOT NULL,
  key_id           uuid NOT NULL,
  started_by       uuid NOT NULL,
  started_by_email text NOT NULL,
  started_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_sessions_grant_idx ON metadata.support_sessions (grant_id);
