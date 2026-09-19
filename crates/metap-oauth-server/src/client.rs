use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::token::{generate_opaque_token, hash_token};

/// A registered third-party OAuth2 client — tenant-scoped (`../metap-docs/docs/roadmap/88-oauth2-authorization-server.md`: a
/// client is only usable by the tenant that registered it, since every isolation boundary
/// elsewhere in this platform is the tenant, not e.g. a global client registry every tenant
/// shares). `client_secret_hash` is intentionally not on this DTO — see [`create_client`]'s doc
/// comment for the one place the raw secret is ever observable.
#[derive(Debug, Clone)]
pub struct OAuthClient {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub client_id: String,
    pub name: String,
    pub redirect_uris: Vec<String>,
    pub allowed_scopes: Vec<String>,
    pub is_confidential: bool,
    pub revoked_at: Option<chrono::DateTime<chrono::Utc>>,
    /// The `users` row this client acts as for the `client_credentials` grant — `None` only for a
    /// client registered before that grant existed (see `crates/migrations/
    /// 0036_oauth2_client_credentials.sql`'s own doc comment); every client `create_client` mints
    /// today gets one eagerly. Starts with zero `user_roles` like any other principal — this
    /// column makes the identity exist, it grants nothing by itself (deny-by-default, same as
    /// every other user in this platform).
    pub service_user_id: Option<Uuid>,
}

/// What [`get_client_by_client_id`] returns — carries the secret hash `metap-http`'s token
/// endpoint needs to verify client authentication against, kept off [`OAuthClient`] itself so
/// nothing that only needs the public client shape (`list_clients`) can even accidentally touch
/// a hash.
pub struct ClientWithSecret {
    pub client: OAuthClient,
    pub client_secret_hash: String,
}

pub struct CreateClientInput {
    pub tenant_id: Uuid,
    pub name: String,
    pub redirect_uris: Vec<String>,
    pub allowed_scopes: Vec<String>,
    pub is_confidential: bool,
    /// The caller (`metap-http`) provisions this `users` row *before* calling `create_client` —
    /// this crate has no `metap-auth` dependency and doesn't know how to provision a user itself,
    /// only how to record which one a client acts as.
    pub service_user_id: Uuid,
}

fn row_to_client(row: &sqlx::postgres::PgRow) -> Result<OAuthClient, sqlx::Error> {
    Ok(OAuthClient {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        client_id: row.try_get("client_id")?,
        name: row.try_get("name")?,
        redirect_uris: row.try_get("redirect_uris")?,
        allowed_scopes: row.try_get("allowed_scopes")?,
        is_confidential: row.try_get("is_confidential")?,
        revoked_at: row.try_get("revoked_at")?,
        service_user_id: row.try_get("service_user_id")?,
    })
}

/// Registers a new client and returns `(client, client_secret)` — **the only time the raw
/// secret is ever available**, mirroring `SecretStore`'s own write-only credential discipline
/// elsewhere in this platform: only a hash is persisted (`token::hash_token`), so a caller who
/// loses the value returned here has to rotate (revoke + recreate), not "look it up again".
/// `client_id` is a separate, non-secret opaque identifier (safe to log, appear in a redirect
/// URL's query string, etc.) — generated the same way as the secret purely for uniform,
/// sufficiently-random uniqueness, not because it needs secrecy.
pub async fn create_client<'e>(
    executor: impl PgExecutor<'e>,
    input: CreateClientInput,
) -> anyhow::Result<(OAuthClient, String)> {
    let client_id = format!("mcl_{}", generate_opaque_token());
    let client_secret = generate_opaque_token();
    let client_secret_hash = hash_token(&client_secret);

    let row = sqlx::query(
        "INSERT INTO oauth_clients \
            (tenant_id, client_id, client_secret_hash, name, redirect_uris, allowed_scopes, is_confidential, \
             service_user_id) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8) \
         RETURNING id, tenant_id, client_id, name, redirect_uris, allowed_scopes, is_confidential, revoked_at, \
                   service_user_id",
    )
    .bind(input.tenant_id)
    .bind(&client_id)
    .bind(&client_secret_hash)
    .bind(&input.name)
    .bind(&input.redirect_uris)
    .bind(&input.allowed_scopes)
    .bind(input.is_confidential)
    .bind(input.service_user_id)
    .fetch_one(executor)
    .await?;

    Ok((row_to_client(&row)?, client_secret))
}

/// Looked up by the wire-visible `client_id` (not the internal `id`) — this is what
/// `GET /oauth/authorize` and `POST /oauth/token` receive from the caller. Includes the secret
/// hash (`pub(crate)`, not part of the public [`OAuthClient`] DTO) so `metap-http`'s token
/// endpoint can verify a presented `client_secret` without a second query.
pub async fn get_client_by_client_id<'e>(
    executor: impl PgExecutor<'e>,
    client_id: &str,
) -> anyhow::Result<Option<ClientWithSecret>> {
    let row = sqlx::query(
        "SELECT id, tenant_id, client_id, client_secret_hash, name, redirect_uris, allowed_scopes, \
                is_confidential, revoked_at, service_user_id \
         FROM oauth_clients WHERE client_id = $1",
    )
    .bind(client_id)
    .fetch_optional(executor)
    .await?;
    row.map(|row| {
        Ok(ClientWithSecret {
            client: row_to_client(&row)?,
            client_secret_hash: row.try_get("client_secret_hash")?,
        })
    })
    .transpose()
}

/// Every non-revoked client for a tenant's own admin UI (`GET /admin/oauth/clients`) — secret
/// hash never selected, so there is no `ClientWithSecret`-shaped row this function could even
/// accidentally leak.
pub async fn list_clients<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid) -> anyhow::Result<Vec<OAuthClient>> {
    let rows = sqlx::query(
        "SELECT id, tenant_id, client_id, name, redirect_uris, allowed_scopes, is_confidential, revoked_at, \
                service_user_id \
         FROM oauth_clients WHERE tenant_id = $1 AND revoked_at IS NULL ORDER BY created_at",
    )
    .bind(tenant_id)
    .fetch_all(executor)
    .await?;
    rows.iter()
        .map(row_to_client)
        .map(|r| r.map_err(anyhow::Error::from))
        .collect()
}

/// Marks a client revoked — scoped to `tenant_id` so an admin can never revoke another tenant's
/// client by guessing its internal id. Deliberately doesn't cascade-revoke that client's live
/// refresh tokens (`oauth_refresh_tokens` has no `ON DELETE`/status trigger tied to this): a
/// revoked client can no longer complete `POST /oauth/token` at all (client auth itself fails
/// first, before any refresh-token lookup), which already closes off further access — see
/// `refresh::consume_refresh_token`'s doc comment for where that check lives.
pub async fn revoke_client<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid, id: Uuid) -> anyhow::Result<bool> {
    let result = sqlx::query(
        "UPDATE oauth_clients SET revoked_at = now() WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL",
    )
    .bind(id)
    .bind(tenant_id)
    .execute(executor)
    .await?;
    Ok(result.rows_affected() > 0)
}

/// `true` iff `raw_secret` hashes to this client's stored secret and the client isn't revoked —
/// the one check `POST /oauth/token`'s client authentication needs, kept here next to the hash
/// it compares against rather than duplicated at the HTTP layer.
pub fn verify_client_secret(client: &ClientWithSecret, raw_secret: &str) -> bool {
    client.client.revoked_at.is_none() && hash_token(raw_secret) == client.client_secret_hash
}
