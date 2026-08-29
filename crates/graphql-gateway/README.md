# graphql-gateway

The real BFF (Backend-for-Frontend): one GraphQL schema aggregated across several
**separately-deployed** microservices — in this repo's demo setup, `apps/jira-server` and
`apps/crm-server`. This is distinct from the per-service GraphQL each of those binaries already
exposes on their own port (`metap-graphql-http`, Phase 49): a query against `jira-server`'s own
`/graphql` can only ever touch `jira.*` entities. A query against this gateway's `/graphql` can
touch entities from *any* configured upstream in one request.

## Why a separate binary

This gateway owns no entity, no Postgres, no `CrudService`. Every record read/write it serves is
a remote gRPC call (`metap_grpc::GrpcBackend`) to whichever upstream microservice actually owns
that entity, routed by name (`metap_graphql::CompositeBackend`, see `src/schema_builder.rs`).
This is the "local-vs-remote dispatch" seam `docs/architectures/04-strategy.md` names as
intentionally undesigned until a real split-deploy trigger existed — this binary is that trigger.

## Boot sequence

1. Read `UPSTREAM_<N>_{NAME,GRPC_ADDR,METADATA_URL,SERVICE_JWT}` env vars (see `.env.example`),
   `N = 1, 2, ...` until the first missing `_NAME`.
2. For each upstream: `GET {METADATA_URL}` (that service's own `GET /metadata/entities`, bearer
   `SERVICE_JWT`) to discover its entities, and connect one `GrpcBackend` to `GRPC_ADDR`.
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
(`AUTH_JWT_PUBLIC_KEY_PATH`) to reach `/graphql` at all. That is the only thing this gateway's
auth does — it does **not** forward the caller's identity to either upstream. Every call this
gateway makes to a given upstream authenticates as that upstream's own fixed
`UPSTREAM_<N>_SERVICE_JWT`, the same "one static, pre-minted service identity" pattern
`cron-scheduler`'s `CRON_SERVICE_JWT` already established in this codebase. Real permission
enforcement still happens exactly where it did before this gateway existed — inside each
upstream's own `CrudService`/`PermissionService`, once the gRPC call actually lands there.

Propagating the original caller's identity through to each upstream would need a shared JWKS
trust root every upstream verifies against (`metap-jwks` exists as a library, but no upstream's
`GrpcRecordService` in this repo is wired to `TokenVerifier::Jwks` today) — deliberately out of
scope for this first version.

## Running it locally against this repo's demo apps

```bash
# 1. Start jira-server and crm-server with gRPC enabled (see each app's .env.example):
#    GRPC_ENABLED=true, plus each app's own gRPC port.
cd apps/jira-server && GRPC_ENABLED=true pnpm dev:rs   # REST :3100, gRPC :3101
cd apps/crm-server && GRPC_ENABLED=true pnpm dev:rs    # REST :3000, gRPC :3001

# 2. Mint one service JWT per upstream, signed by that upstream's own keypair.
cd apps/jira-server && pnpm mint:jira-token <tenantId> <userId>
cd apps/crm-server && pnpm mint-token <tenantId> <userId>

# 3. Generate this gateway's own keypair and fill in crates/graphql-gateway/.env
#    (UPSTREAM_1_*/UPSTREAM_2_* with the two tokens above).

# 4. Run the gateway.
cargo run -p metap-graphql-gateway
```

Open `http://localhost:4000/graphql/playground` and query fields from both a `jira.*` and a
`crm.*` entity in one request — that single response containing both is the actual proof this is
a BFF, not two services glued together at the frontend.
