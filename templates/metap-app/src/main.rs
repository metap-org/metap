//! Boot sequence for {{project-name}}: `MetapApp` connects to Postgres, registers/reconciles
//! entities, and serves — see `example_entity.rs` for a starting point to replace with your own
//! entity, and `metap`'s own doc comment (`crates/metap/src/lib.rs` in the metap repo) for what
//! else is reachable through its namespaced modules (`metap::query`, `metap::workflow`, etc.).
//!
//! Reads config from the environment (or a `.env` file in the current directory — see
//! `.env.example`). Run from this directory so that resolves the way you expect.

mod example_entity;

use metap::prelude::*;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // `MetapApp` (`metap-app` crate) replaces what used to be ~100 lines of hand-wired
    // boilerplate here: `bootstrap_platform` (Postgres pool, tenant `Router`, `PermissionService`,
    // JWT keypair), entity registration + reconcile (in the given order — load-bearing whenever a
    // second entity references this one via `Reference`) + metadata-drift/index-reconcile checks,
    // `AppState::new`'s 7 positional parameters, and bind+serve. See `MetapApp`'s own doc comment
    // for the opt-in pieces this template doesn't use (`.with_audit()`, `.with_jwks[_publish]()`,
    // `.with_grpc(port)`, `.with_extra_routes(...)` for mounting `metap-lowcode-http`/
    // `metap-graphql-http`/a custom router, `.with_state_middleware(...)`).
    MetapApp::bootstrap(load_config()?)
        .await?
        .with_entities(vec![example_entity::example_entity()])
        .await?
        .serve()
        .await
}
