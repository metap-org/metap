-- Who owns an email domain (roadmap 115 M7, domain ownership). A tenant that names a domain in its
-- single sign-on settings gets a pending row with a token to publish as a DNS TXT record; the domain
-- counts (for routing a sign-in to the tenant, and for matching or creating members by address) only
-- once the record is seen. At most one tenant owns a domain: the first to prove it.
CREATE TABLE metadata.sso_domains (
  tenant_id   uuid        NOT NULL,
  domain      text        NOT NULL,
  token       text        NOT NULL,
  verified_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, domain)
);
CREATE UNIQUE INDEX sso_domains_one_owner_idx ON metadata.sso_domains (domain) WHERE verified_at IS NOT NULL;

-- Tenants that already named domains get them as pending, each with its own token: they keep
-- signing in the people already tied to their provider, and verify a domain to route new sign-ins.
INSERT INTO metadata.sso_domains (tenant_id, domain, token)
SELECT c.tenant_id, lower(d.value), replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '')
FROM metadata.tenant_auth_configs c
CROSS JOIN LATERAL jsonb_array_elements_text(coalesce(c.config->'emailDomains', '[]'::jsonb)) AS d(value)
WHERE c.provider_kind = 'oidc'
ON CONFLICT DO NOTHING;
