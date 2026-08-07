//! Replaces `packages/core/scripts/{generate-dev-jwt-keypair,mint-dev-token,seed-admin}.mjs`
//! — three tiny dev scripts consolidated into one binary with subcommands, since none of
//! them is more than a few lines and no separate `packages/core` remains to host them.

use metap_peripherals::assign_role;
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

fn usage() -> ! {
    eprintln!("Usage:");
    eprintln!("  dev-tools gen-keys [dir]                         (default dir: ./keys)");
    eprintln!("  dev-tools mint-token [tenantId] [userId]         (RS256, reads ./keys/dev-jwt-private.pem)");
    eprintln!("  dev-tools seed-admin <tenantId> <userId>");
    std::process::exit(1);
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args: Vec<String> = std::env::args().collect();
    match args.get(1).map(String::as_str) {
        Some("gen-keys") => gen_keys(args.get(2).cloned().unwrap_or_else(|| "keys".to_string())),
        Some("mint-token") => mint_token(&args),
        Some("seed-admin") => seed_admin(&args).await,
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

#[derive(serde::Serialize)]
struct Claims {
    sub: String,
    #[serde(rename = "tenantId")]
    tenant_id: String,
    exp: usize,
}

/// Mirrors `mint-dev-token.mjs`: same default tenant/user IDs, same 1h expiry, same
/// `keys/dev-jwt-private.pem` path (relative to cwd, matching that script's convention).
fn mint_token(args: &[String]) -> anyhow::Result<()> {
    let tenant_id = args.get(2).cloned().unwrap_or_else(|| "00000000-0000-0000-0000-000000000001".to_string());
    let user_id = args.get(3).cloned().unwrap_or_else(|| "00000000-0000-0000-0000-000000000002".to_string());

    let private_pem = std::fs::read_to_string("keys/dev-jwt-private.pem")
        .map_err(|e| anyhow::anyhow!("failed to read keys/dev-jwt-private.pem: {e}"))?;
    let key = jsonwebtoken::EncodingKey::from_rsa_pem(private_pem.as_bytes())?;

    let claims = Claims {
        sub: user_id,
        tenant_id,
        exp: expiry_in_one_hour()?,
    };
    let token = jsonwebtoken::encode(
        &jsonwebtoken::Header::new(jsonwebtoken::Algorithm::RS256),
        &claims,
        &key,
    )?;
    println!("{token}");
    Ok(())
}

fn expiry_in_one_hour() -> anyhow::Result<usize> {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?;
    Ok((now.as_secs() + 3600) as usize)
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
    let database_url = std::env::var("DATABASE_URL")
        .map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;
    let pool = PgPoolOptions::new().max_connections(1).connect(&database_url).await?;

    let tenant_id: Uuid = tenant_id.parse()?;
    let user_id: Uuid = user_id.parse()?;
    assign_role(&pool, tenant_id, user_id, "admin", None).await?;

    println!("Granted 'admin' role to user {user_id} in tenant {tenant_id}.");
    Ok(())
}
