//! **Chaos test — real fault injection, not a mock.** Pauses a real, throwaway Postgres
//! container mid-request to characterize what a `DedicatedDb` tenant's caller actually
//! experiences when that tenant's own database becomes unreachable — `Router::pool_for`/
//! `dedicated_pool` (`src/router.rs`) have no retry and no circuit breaker (confirmed by
//! survey), so this documents the current behavior (does the call fail cleanly within a bounded
//! time, or hang) rather than asserting a resilience SLA that doesn't exist yet. See
//! `testing/disruptive/README.md` for the philosophy and the full scenario table.
//!
//! Needs its own **throwaway Postgres container** (not the shared dev `metap-postgres-1`) —
//! pausing that would break every other e2e test racing this one. `#[ignore]`d like every other
//! e2e test in this crate, **plus** needs the `docker` CLI on PATH. Run:
//! `DATABASE_URL=postgres://metap:metap@localhost:5433/metap \
//!  cargo test -p metap-control --test chaos_postgres -- --ignored --nocapture`
//! (after `docker compose up -d postgres` from the `metap` repo root — that's the platform's own
//! shared registry DB `control.tenants` lives in; this test's own throwaway container is what
//! actually gets paused).

use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

use metap_control::{EnvStore, PostgresTenantRegistry, RegistryCache, Router, TenantId};
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

const THROWAWAY_HOST_PORT: u16 = 18433;

async fn connect_platform_pool() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this chaos test");
    PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .expect("connect to dev postgres")
}

fn docker(args: &[&str]) {
    let status = Command::new("docker")
        .args(args)
        .status()
        .unwrap_or_else(|e| panic!("docker CLI must be on PATH for this chaos test: {e}"));
    assert!(status.success(), "docker {args:?} exited non-zero");
}

struct ThrowawayPostgres {
    container_name: String,
    dsn: String,
}

impl ThrowawayPostgres {
    async fn start() -> Self {
        let container_name = format!("metap-chaos-test-postgres-{}", Uuid::new_v4().simple());
        docker(&[
            "run",
            "-d",
            "--name",
            &container_name,
            "-p",
            &format!("{THROWAWAY_HOST_PORT}:5432"),
            "-e",
            "POSTGRES_USER=metap",
            "-e",
            "POSTGRES_PASSWORD=metap",
            "-e",
            "POSTGRES_DB=metap",
            "postgres:16-alpine",
        ]);
        let dsn = format!("postgres://metap:metap@localhost:{THROWAWAY_HOST_PORT}/metap");

        // Wait for the throwaway server to actually accept connections, not just "container
        // started" — `pg_isready` inside the container avoids a host-side dependency on the
        // Postgres client tools.
        let ready_deadline = std::time::Instant::now() + Duration::from_secs(30);
        loop {
            let ok = Command::new("docker")
                .args(["exec", &container_name, "pg_isready", "-U", "metap"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if ok {
                break;
            }
            assert!(
                std::time::Instant::now() < ready_deadline,
                "throwaway Postgres container never became ready within 30s"
            );
            tokio::time::sleep(Duration::from_millis(500)).await;
        }

        Self { container_name, dsn }
    }

    fn pause(&self) {
        docker(&["pause", &self.container_name]);
    }

    fn unpause(&self) {
        // Best-effort — `unpause` on an already-running (never paused, or already unpaused)
        // container is a Docker no-op error, not something this cleanup path should panic on.
        let _ = Command::new("docker").args(["unpause", &self.container_name]).status();
    }
}

impl Drop for ThrowawayPostgres {
    fn drop(&mut self) {
        // Best-effort teardown — a `Drop` impl can't be `async`/can't propagate failure
        // meaningfully, and a leaked chaos-test container is a cheap, obvious thing to notice
        // and clean up by hand (`docker ps -a | grep metap-chaos-test-postgres`) rather than
        // worth architecting an async-drop guard for.
        let _ = Command::new("docker").args(["rm", "-f", &self.container_name]).status();
    }
}

#[tokio::test]
#[ignore = "chaos: pauses a real throwaway Postgres container — needs `docker` on PATH, see testing/disruptive/README.md"]
async fn dedicated_tenant_request_fails_within_a_bounded_time_when_its_postgres_is_paused() {
    let platform_pool = connect_platform_pool().await;
    let throwaway = ThrowawayPostgres::start().await;

    let tenant_id = Uuid::new_v4();
    let dsn_secret_ref = format!("METAP_CHAOS_TEST_DSN_{}", tenant_id.simple());
    std::env::set_var(&dsn_secret_ref, &throwaway.dsn);
    sqlx::query(
        "INSERT INTO control.tenants (id, tier, strategy, dsn_secret_ref, status) \
         VALUES ($1, 'paid', 'dedicated_db', $2, 'active')",
    )
    .bind(tenant_id)
    .bind(&dsn_secret_ref)
    .execute(&platform_pool)
    .await
    .expect("insert control.tenants row");

    // Happy path first — proves the throwaway container and tenant registration actually work
    // before anything is paused, so a false pass below can't be mistaken for chaos working.
    let registry = Arc::new(PostgresTenantRegistry::new(platform_pool.clone()));
    let router = Router::new(platform_pool.clone(), RegistryCache::new(registry), Arc::new(EnvStore));
    router
        .pool_for(TenantId(tenant_id))
        .await
        .expect("pool_for must succeed against the throwaway Postgres while it's healthy");

    println!("chaos: pausing {}...", throwaway.container_name);
    throwaway.pause();

    // A fresh Router (fresh `dedicated_pools` moka cache) is required here — the happy-path call
    // above may have already opened and cached a live connection, and pausing a container
    // freezes its process without closing existing TCP sessions, so a cached pool could still
    // "work" (queueing on an already-open, now-frozen connection) rather than exercising the
    // actual connect-time failure this test targets.
    let registry2 = Arc::new(PostgresTenantRegistry::new(platform_pool.clone()));
    let router2 = Router::new(platform_pool.clone(), RegistryCache::new(registry2), Arc::new(EnvStore));

    // `Router::pool_for`/`dedicated_pool` (src/router.rs) set no `acquire_timeout`/
    // `connect_timeout` on the `PgPoolOptions` it builds — this bound is the test's own, not a
    // guarantee the production code makes. If this assertion ever fails because the call hung
    // past 35s, that is itself the finding: no retry AND no timeout, a real gap worth fixing
    // (add an explicit connect timeout), not a flaky test to loosen.
    let outcome = tokio::time::timeout(Duration::from_secs(35), router2.pool_for(TenantId(tenant_id))).await;

    throwaway.unpause();

    match outcome {
        Ok(Ok(_)) => panic!(
            "pool_for unexpectedly SUCCEEDED against a paused Postgres container — either the \
             pause didn't take effect, or a cached connection was reused instead of a fresh one"
        ),
        Ok(Err(e)) => {
            println!("chaos: pool_for failed cleanly within 35s while paused, as expected: {e}");
        }
        Err(_) => {
            panic!(
                "pool_for neither succeeded nor failed within 35s against a paused Postgres — it \
                 hung. Router::pool_for has no retry/circuit-breaker (confirmed by code survey) \
                 and, as of this test, also no explicit connect timeout — a caller-facing HTTP \
                 request down this path would hang for however long the underlying TCP stack \
                 takes to notice, not fail fast."
            );
        }
    }

    std::env::remove_var(&dsn_secret_ref);
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&platform_pool)
        .await
        .ok();
}
