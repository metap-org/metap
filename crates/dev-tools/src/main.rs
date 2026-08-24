//! Replaces `packages/core/scripts/{generate-dev-jwt-keypair,mint-dev-token,seed-admin}.mjs`
//! — three tiny dev scripts consolidated into one binary with subcommands, since none of
//! them is more than a few lines and no separate `packages/core` remains to host them.

use std::sync::Arc;

use metap_peripherals::assign_role;
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

/// `seed_admin`/`create_user` write into `users`/`user_roles` — for a `DedicatedDb`-strategy
/// tenant those tables live only in that tenant's own database, never in `DATABASE_URL`'s shared
/// platform pool. Found live (2026-08-24): running either command from a directory whose `.env`
/// happens to point at the platform DB silently wrote the row into the *wrong* database for a
/// dedicated tenant — no error, just a row nothing ever queries, because neither command
/// resolved the tenant through `Router` the way every other write path in this repo does. This
/// builds the same kind of `Router` `apps/*-server/src/main.rs` does, so these two CLI paths
/// can't diverge from what a real request would resolve to. `EnvStore` only (not the AppRole/
/// Vault branching `apps/crm-server/src/main.rs` has) — a `Vault`-backed dedicated tenant's DSN
/// can still be seeded by hand via `vault-put-dsn` first, same as it always could.
async fn router_for(shared_pool: sqlx::PgPool) -> metap_control::Router {
    let tenant_registry = Arc::new(metap_control::PostgresTenantRegistry::new(shared_pool.clone()));
    metap_control::Router::new(
        shared_pool,
        metap_control::RegistryCache::new(tenant_registry),
        Arc::new(metap_control::EnvStore),
    )
}

fn usage() -> ! {
    eprintln!("Usage:");
    eprintln!("  dev-tools gen-keys [dir]                         (default dir: ./keys)");
    eprintln!("  dev-tools mint-token [tenantId] [userId]         (RS256, reads ./keys/dev-jwt-private.pem)");
    eprintln!("  dev-tools seed-admin <tenantId> <userId>");
    eprintln!("  dev-tools create-user <tenantId> <email> <password>  (local login, argon2id)");
    eprintln!("  dev-tools provision-tenant <tenantId> schema <adminEmail> <adminPassword>");
    eprintln!(
        "  dev-tools provision-tenant <tenantId> dedicated_db <dsnSecretRefName> <dedicatedDatabaseUrl> <adminEmail> <adminPassword>"
    );
    eprintln!("  dev-tools bootstrap-platform-admin <email> <password>");
    eprintln!("  dev-tools vault-put-dsn <dsnSecretRef> <dsn>                     (reads VAULT_ADDR/VAULT_TOKEN)");
    std::process::exit(1);
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args: Vec<String> = std::env::args().collect();
    match args.get(1).map(String::as_str) {
        Some("gen-keys") => gen_keys(args.get(2).cloned().unwrap_or_else(|| "keys".to_string())),
        Some("mint-token") => mint_token(&args),
        Some("seed-admin") => seed_admin(&args).await,
        Some("create-user") => create_user(&args).await,
        Some("provision-tenant") => provision_tenant(&args).await,
        Some("bootstrap-platform-admin") => bootstrap_platform_admin(&args).await,
        Some("vault-put-dsn") => vault_put_dsn(&args).await,
        _ => usage(),
    }
}

/// Mirrors `generate-dev-jwt-keypair.mjs`: an RSA 2048 keypair via `openssl` (already a
/// tested dependency of this repo's dev workflow — see
/// `crates/metap-http/tests/http_server.rs`), not a new Rust crypto crate dependency for
/// something this infrequent.
fn gen_keys(dir: String) -> anyhow::Result<()> {
    std::fs::create_dir_all(&dir)?;
    let private_path = format!("{dir}/dev-jwt-private.pem");
    let public_path = format!("{dir}/dev-jwt-public.pem");

    let status = std::process::Command::new("openssl")
        .args(["genrsa", "-out", &private_path, "2048"])
        .status()?;
    anyhow::ensure!(status.success(), "openssl genrsa failed");

    let status = std::process::Command::new("openssl")
        .args(["rsa", "-in", &private_path, "-pubout", "-out", &public_path])
        .status()?;
    anyhow::ensure!(status.success(), "openssl rsa -pubout failed");

    println!("Generated dev JWT keypair in ./{dir} (gitignored).");
    println!("Set in .env: AUTH_JWT_PUBLIC_KEY_PATH=./{dir}/dev-jwt-public.pem");
    Ok(())
}

/// Mirrors `mint-dev-token.mjs`: same default tenant/user IDs, same 1h expiry, same
/// `keys/dev-jwt-private.pem` path (relative to cwd, matching that script's convention).
/// Delegates the actual encoding to `metap_peripherals::mint_jwt` — the same function
/// `POST /auth/login` calls — so this CLI and a real login can't mint differently-shaped
/// tokens.
///
/// The default tenant/user ids are **not** a registered `control.tenants` row — found live
/// (2026-08-24): calling this with no args against an app whose only real tenant is a
/// `dedicated_db` one (e.g. `apps/jira-server`) mints a token for a tenant `Router` has never
/// heard of, which falls back to the shared platform schema — every request then 500s with a
/// confusing "relation does not exist" instead of an auth error, since the token itself decodes
/// fine. No file I/O here to check registration against (this only touches the JWT keypair,
/// never the DB), so the fix is a loud warning on the default path rather than a silent trap.
fn mint_token(args: &[String]) -> anyhow::Result<()> {
    let tenant_id: Uuid = match args.get(2).map(String::as_str) {
        Some(id) => id.parse()?,
        None => {
            eprintln!(
                "Warning: no tenantId given, defaulting to 00000000-0000-0000-0000-000000000001 \
                 — this is NOT a provisioned tenant for most apps (check the app's .env for its \
                 real tenant id, e.g. JIRA_TENANT_ID). Pass it explicitly: \
                 dev-tools mint-token <tenantId> [userId]"
            );
            "00000000-0000-0000-0000-000000000001".parse()?
        }
    };
    let user_id: Uuid = args
        .get(3)
        .map(String::as_str)
        .unwrap_or("00000000-0000-0000-0000-000000000002")
        .parse()?;

    let private_pem = std::fs::read_to_string("keys/dev-jwt-private.pem")
        .map_err(|e| anyhow::anyhow!("failed to read keys/dev-jwt-private.pem: {e}"))?;

    let token = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600)?;
    println!("{token}");
    Ok(())
}

/// Mirrors `seed-admin.mjs`, via `metap_peripherals::assign_role` (the same function
/// `crates/metap-peripherals`'s e2e tests already verify against the real dev Postgres)
/// instead of a hand-rolled query.
async fn seed_admin(args: &[String]) -> anyhow::Result<()> {
    let (Some(tenant_id), Some(user_id)) = (args.get(2), args.get(3)) else {
        eprintln!("Usage: dev-tools seed-admin <tenantId> <userId>");
        std::process::exit(1);
    };
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    let pool = PgPoolOptions::new().max_connections(1).connect(&database_url).await?;

    let tenant_id: Uuid = tenant_id.parse()?;
    let user_id: Uuid = user_id.parse()?;

    let router = router_for(pool).await;
    let mut tx = router
        .begin(tenant_id.into())
        .await
        .map_err(|e| anyhow::anyhow!("failed to resolve tenant {tenant_id}'s database: {e}"))?;
    assign_role(&mut *tx, tenant_id, user_id, "admin", None).await?;
    tx.commit().await?;

    println!("Granted 'admin' role to user {user_id} in tenant {tenant_id}.");
    Ok(())
}

/// Dev-seeding counterpart to `POST /admin/users` (`crates/metap-http/src/routes/admin.rs`) —
/// both call `metap_peripherals::create_user`, so a seeded dev user and an admin-provisioned
/// one get their password hashed identically.
async fn create_user(args: &[String]) -> anyhow::Result<()> {
    let (Some(tenant_id), Some(email), Some(password)) = (args.get(2), args.get(3), args.get(4)) else {
        eprintln!("Usage: dev-tools create-user <tenantId> <email> <password>");
        std::process::exit(1);
    };
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    let pool = PgPoolOptions::new().max_connections(1).connect(&database_url).await?;

    let tenant_id: Uuid = tenant_id.parse()?;

    let router = router_for(pool).await;
    let mut tx = router
        .begin(tenant_id.into())
        .await
        .map_err(|e| anyhow::anyhow!("failed to resolve tenant {tenant_id}'s database: {e}"))?;
    let user = metap_peripherals::create_user(&mut *tx, tenant_id, email, password).await?;
    tx.commit().await?;

    println!("Created user {} ({email}) in tenant {tenant_id}.", user.id);
    Ok(())
}

/// Registers a `control.tenants` row (`metap_control::PostgresTenantRegistry::provision`) and
/// creates the tenant's first admin user — the bootstrap path for a tenant, mirroring how
/// `seed_admin`/`create_user` above already bootstrap a user/role without going through
/// `AdminContext` (there's no admin yet to authorize an HTTP call). No `POST /admin/tenants`
/// exists for the same reason `AdminContext` can't provision a *new* tenant: it only authorizes
/// actions inside the caller's own tenant (`PermissionService::scoped_tenant`), and there's no
/// cross-tenant "platform admin" concept — see `docs/multi-tenant-platform-design.md` §2.2's
/// implementation note.
///
/// `strategy=schema` always pins `schema_name` to `"public"` — schema-per-tenant has no real
/// isolation yet (the shared `records`/`users`/... tables only exist in `public` until
/// table-per-entity, §3, lands; routing a tenant to any other schema today would just break
/// every query with "relation does not exist"). `strategy=dedicated_db` is the only strategy
/// with real isolation right now: a genuinely separate Postgres database, migrated the same as
/// the primary one.
async fn provision_tenant(args: &[String]) -> anyhow::Result<()> {
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    let pool = PgPoolOptions::new().max_connections(1).connect(&database_url).await?;
    let registry = metap_control::PostgresTenantRegistry::new(pool.clone());

    match args.get(3).map(String::as_str) {
        Some("schema") => {
            let (Some(tenant_id), Some(email), Some(password)) = (args.get(2), args.get(4), args.get(5)) else {
                eprintln!("Usage: dev-tools provision-tenant <tenantId> schema <adminEmail> <adminPassword>");
                std::process::exit(1);
            };
            let tenant_id: Uuid = tenant_id.parse()?;

            let provisioned =
                metap_control::provision_schema_tenant(&pool, &registry, tenant_id, email, password).await?;

            println!(
                "Provisioned tenant {tenant_id} (trial, schema=public), admin user {} ({email}).",
                provisioned.admin_user_id
            );
            print_deny_by_default_note();
        }
        Some("dedicated_db") => {
            let (Some(tenant_id), Some(dsn_secret_ref), Some(dedicated_url), Some(email), Some(password)) =
                (args.get(2), args.get(4), args.get(5), args.get(6), args.get(7))
            else {
                eprintln!(
                    "Usage: dev-tools provision-tenant <tenantId> dedicated_db <dsnSecretRefName> <dedicatedDatabaseUrl> <adminEmail> <adminPassword>"
                );
                std::process::exit(1);
            };
            let tenant_id: Uuid = tenant_id.parse()?;

            let provisioned = metap_control::provision_dedicated_db_tenant(
                &registry,
                tenant_id,
                dsn_secret_ref,
                dedicated_url,
                email,
                password,
            )
            .await?;

            println!(
                "Provisioned tenant {tenant_id} (paid, dedicated_db), admin user {} ({email}).",
                provisioned.admin_user_id
            );
            println!(
                "Before this tenant can be routed to, set env var {dsn_secret_ref}={dedicated_url} \
                 for the crm-server process (Router's EnvStore looks it up by this exact name)."
            );
            print_deny_by_default_note();
        }
        _ => {
            eprintln!("Usage: dev-tools provision-tenant <tenantId> <schema|dedicated_db> ...");
            std::process::exit(1);
        }
    }

    Ok(())
}

/// Bootstraps the very first platform-superadmin (Phase 16 Giai đoạn 3, `docs/roadmap.md`) —
/// same con-gà-quả-trứng bootstrap as `seed_admin`/`create_user` above, CLI-only because no
/// platform-admin exists yet to authorize an HTTP call. Creates a user in
/// `metap_control::PLATFORM_TENANT_ID` (a sentinel tenant, never a real `control.tenants` row —
/// see that constant's doc comment) and grants it the `"platform_admin"` role, the one
/// `metap-http`'s `PlatformAdminContext` extractor checks for. The resulting user mints a token
/// exactly like any other (`pnpm mint-token <platformTenantId> <userId>`) — there is no second
/// auth mechanism to bootstrap here, only a role grant in a reserved tenant.
async fn bootstrap_platform_admin(args: &[String]) -> anyhow::Result<()> {
    let (Some(email), Some(password)) = (args.get(2), args.get(3)) else {
        eprintln!("Usage: dev-tools bootstrap-platform-admin <email> <password>");
        std::process::exit(1);
    };
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    let pool = PgPoolOptions::new().max_connections(1).connect(&database_url).await?;

    let tenant_id = metap_control::PLATFORM_TENANT_ID;
    let user = metap_peripherals::create_user(&pool, tenant_id, email, password).await?;
    assign_role(&pool, tenant_id, user.id, "platform_admin", None).await?;

    println!("Created platform-admin user {} ({email}).", user.id);
    println!("Mint a token for it with: pnpm mint-token {tenant_id} {}", user.id);
    Ok(())
}

/// Populates Vault for a `dedicated_db` tenant (Phase 16 Giai đoạn 4, `docs/roadmap.md`) — the
/// Vault-backed counterpart to "set env var `<dsnSecretRef>`" that `provision-tenant ...
/// dedicated_db` already prints for `EnvStore`. Independent of `provision-tenant`: run it
/// before or after, in either order — `Router` only resolves the DSN when it actually routes a
/// request to that tenant.
async fn vault_put_dsn(args: &[String]) -> anyhow::Result<()> {
    let (Some(dsn_secret_ref), Some(dsn)) = (args.get(2), args.get(3)) else {
        eprintln!("Usage: dev-tools vault-put-dsn <dsnSecretRef> <dsn>");
        std::process::exit(1);
    };
    dotenvy::dotenv().ok();
    let vault_addr = std::env::var("VAULT_ADDR").map_err(|_| anyhow::anyhow!("VAULT_ADDR is required"))?;
    let vault_token = std::env::var("VAULT_TOKEN").map_err(|_| anyhow::anyhow!("VAULT_TOKEN is required"))?;

    let store = metap_control::VaultStore::new(&vault_addr, &vault_token)?;
    store.put_dsn(dsn_secret_ref, dsn).await?;

    println!("Wrote dsn_secret_ref \"{dsn_secret_ref}\" to Vault at {vault_addr}.");
    Ok(())
}

fn print_deny_by_default_note() {
    println!(
        "Note: no policies exist yet for this tenant — PermissionService denies any action on \
         an entity/action pair with zero policies for non-admin callers (deny-by-default, \
         admin always bypasses). The admin user just created can already do everything; any \
         other user in this tenant is denied everything until you grant policies, e.g. \
         POST /admin/policies/seed-defaults."
    );
}
