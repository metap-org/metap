# graphql-gateway

The real BFF (Backend-for-Frontend): one GraphQL schema aggregated across several
**separately-deployed** microservices — in the demo setup, `../metap-demo-jira` and
`../metap-demo-crm` (sibling repos, moved out of this one 2026-08-31 — see the root `CLAUDE.md`'s
"No example apps in this repo" note). This is distinct from the per-service GraphQL each of those
binaries already exposes on their own port (`metap-graphql-http`, Phase 49): a query against
`metap-demo-jira`'s own `/graphql` can only ever touch `jira.*` entities. A query against this
gateway's `/graphql` can touch entities from *any* configured upstream in one request.

## Why a separate binary

This gateway owns no entity, no Postgres, no `CrudService`. Every record read/write it serves is
a remote gRPC call (`metap_grpc::GrpcBackend`) to whichever upstream microservice actually owns
that entity, routed by name (`metap_graphql::CompositeBackend`, see `src/schema_builder.rs`).
This is the "local-vs-remote dispatch" seam `docs/architectures/04-strategy.md` names as
intentionally undesigned until a real split-deploy trigger existed — this binary is that trigger.

## Boot sequence

1. Read `UPSTREAM_<N>_{NAME,GRPC_ADDR,METADATA_URL,LOGIN_URL,SERVICE_EMAIL,SERVICE_PASSWORD}` env
   vars (see `.env.example`), `N = 1, 2, ...` until the first missing `_NAME`.
2. For each upstream: log into `LOGIN_URL` (that service's own `POST /auth/login`) as
   `SERVICE_EMAIL`/`SERVICE_PASSWORD`, `GET {METADATA_URL}` (bearer the token just obtained) to
   discover its entities, and connect one `GrpcBackend` to `GRPC_ADDR`.
3. Register every discovered entity into one shared `MetadataRegistry` — fails fast at boot if
   two upstreams claim the same entity name.
4. Build the schema (`metap_graphql::build_schema`) over that merged registry, backed by a
   `CompositeBackend` that routes each entity's calls to the `GrpcBackend` of the upstream that
   owns it.
5. Serve `GET /health`, `POST /graphql`, `GET /graphql/playground` (non-production only) on its
   own minimal `axum` app (`src/server.rs`) — not `metap_http::build_router`/`AppState`, which
   assume a Postgres pool/`CrudService` this binary never has.

## Auth — read this before assuming caller identity propagates

A request needs a Bearer token that decodes against **this gateway's own keypair**
(`AUTH_JWT_PUBLIC_KEY_PATH`) to reach `/graphql` at all. What happens to that identity from there
depends on whether the caller and the upstream share a signing keypair (2026-09-02, updated from
this section's original "never forwards" version):

- **If they share a keypair** (true of every `metap-demo-waf` service today — all 3 plus this
  gateway verify against one dev keypair), the caller's own token is forwarded verbatim to the
  upstream (`RequestContext::forwarded_bearer_token`, set in `src/server.rs::authenticate`) — a
  mutation through this gateway enforces the *real caller's* own permissions/audit trail at the
  upstream, not a service account's.
- **Otherwise** — or for this gateway's own boot-time schema-discovery calls, which have no
  inbound request to forward from — each upstream is called with the identity from that upstream's
  own `UPSTREAM_<N>_SERVICE_EMAIL`/`SERVICE_PASSWORD`: a real user this gateway logs into via that
  upstream's own `POST /auth/login`, refreshed automatically in the background well before the
  token expires (`metap_runtime::service_token::ServiceTokenSource`, re-exported as
  `metap_grpc::ServiceTokenSource` — `cron-scheduler` also uses it directly over REST) — provision
  one with `dev-tools create-user` + `dev-tools seed-admin`. This replaced a hand-minted-once
  `UPSTREAM_<N>_SERVICE_JWT` after that JWT's 1h TTL expired in a running deployment and crashed
  the gateway at boot.

Either way, real permission enforcement happens exactly where it did before this gateway existed —
inside each upstream's own `CrudService`/`PermissionService`, once the gRPC call actually lands
there; this gateway itself does no authorization of its own.

A deployment where the caller and an upstream do *not* share a keypair would need a JWKS trust
root instead of verbatim forwarding for that upstream (`metap-jwks` exists as a library, but no
upstream's `GrpcRecordService` in this repo is wired to `TokenVerifier::Jwks` today) — out of scope
here.

## Running it locally against the sibling demo repos

```bash
# 1. Start metap-demo-jira and metap-demo-crm with gRPC enabled (see each repo's .env.example):
#    GRPC_ENABLED=true, plus each app's own gRPC port.
cd ../metap-demo-jira && GRPC_ENABLED=true cargo run   # REST :3100, gRPC :3101
cd ../metap-demo-crm && GRPC_ENABLED=true cargo run    # REST :3000, gRPC :3001

# 2. Provision one service-account user per upstream (email+password, admin role).
cd ../metap-demo-jira && cargo run --manifest-path ../metap/crates/dev-tools/Cargo.toml -- create-user <tenantId> <email> <password>
cd ../metap-demo-jira && cargo run --manifest-path ../metap/crates/dev-tools/Cargo.toml -- seed-admin <tenantId> <userId>
cd ../metap-demo-crm && cargo run --manifest-path ../metap/crates/dev-tools/Cargo.toml -- create-user <tenantId> <email> <password>
cd ../metap-demo-crm && cargo run --manifest-path ../metap/crates/dev-tools/Cargo.toml -- seed-admin <tenantId> <userId>

# 3. Generate this gateway's own keypair and fill in crates/graphql-gateway/.env
#    (UPSTREAM_1_*/UPSTREAM_2_* with the two accounts above).

# 4. Run the gateway.
cargo run -p metap-graphql-gateway
```

Open `http://localhost:4000/graphql/playground` and query fields from both a `jira.*` and a
`crm.*` entity in one request — that single response containing both is the actual proof this is
a BFF, not two services glued together at the frontend.
