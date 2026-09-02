//! E2E test: a real axum server (REST `build_router` + this crate's `/graphql` merged in exactly
//! as a downstream binary would), hit with real HTTP requests — auth included (a real RS256 JWT,
//! minted and verified, not stubbed). `#[ignore]`d, same convention as
//! `crates/metap-http/tests/http_server.rs` (needs a running Postgres, run explicitly via
//! `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_graphql::SchemaLimits;
use metap_http::{build_router, AppState};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::PermissionService;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

fn test_router(pool: PgPool) -> metap_control::Router {
    let registry = Arc::new(metap_control::PostgresTenantRegistry::new(pool.clone()));
    metap_control::Router::new(
        pool,
        metap_control::RegistryCache::new(registry),
        Arc::new(metap_control::EnvStore),
    )
}

fn tempdir() -> TempDir {
    TempDir::new()
}

struct TempDir(std::path::PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-graphql-http-test-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }

    fn path(&self) -> &std::path::Path {
        &self.0
    }
}

fn openssl_genrsa(dir: &std::path::Path) -> (String, String) {
    let private_path = dir.join("private.pem");
    let public_path = dir.join("public.pem");
    let status = Command::new("openssl")
        .args(["genrsa", "-out"])
        .arg(&private_path)
        .arg("2048")
        .status()
        .expect("openssl genrsa must run for this e2e test");
    assert!(status.success());
    let status = Command::new("openssl")
        .args(["rsa", "-in"])
        .arg(&private_path)
        .args(["-pubout", "-out"])
        .arg(&public_path)
        .status()
        .expect("openssl rsa -pubout must run for this e2e test");
    assert!(status.success());
    (
        std::fs::read_to_string(private_path).unwrap(),
        std::fs::read_to_string(public_path).unwrap(),
    )
}

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_http_orders".to_string(),
        label: "Order".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "name".to_string(),
            label: "Name".to_string(),
            kind: FieldKind::String,
            required: Some(true),
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
            computed: None,
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn graphql_endpoint_requires_auth_and_serves_a_real_mutation_and_query() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();

    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    let token = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600).unwrap();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let registry = Arc::new(registry);
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router(pool.clone()),
    );

    let graphql_routes = metap_graphql_http::router(&state, SchemaLimits::default())
        .unwrap()
        .merge(metap_graphql_http::playground_router("/graphql"));
    let router = build_router(state, &["http://localhost:5173".to_string()], graphql_routes);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(
            listener,
            router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
        )
        .await
        .unwrap();
    });
    let base = format!("http://{addr}");
    let client = reqwest::Client::new();

    // unauthenticated
    let unauthed = client
        .post(format!("{base}/graphql"))
        .json(&json!({ "query": "{ testGqlHttpOrdersList { records { id } } }" }))
        .send()
        .await
        .unwrap();
    assert_eq!(unauthed.status(), 401);

    // `GET /graphql/playground` — the GraphiQL page itself is unauthenticated static HTML (see
    // `playground_router`'s doc comment for why: it sends its own queries to the real, auth-gated
    // `/graphql`, not this route).
    let playground = client.get(format!("{base}/graphql/playground")).send().await.unwrap();
    assert_eq!(playground.status(), 200);
    // Must carry its own, more permissive CSP (unpkg.com script/style, inline script) — not the
    // API-shaped global default `metap-http::security_headers` sets for everything else, which
    // would make the browser block GraphiQL's own scripts (found live testing this manually).
    let csp = playground
        .headers()
        .get("content-security-policy")
        .unwrap()
        .to_str()
        .unwrap()
        .to_string();
    assert!(
        csp.contains("unpkg.com"),
        "expected the playground's own relaxed CSP, got: {csp}"
    );
    let playground_html = playground.text().await.unwrap();
    assert!(
        playground_html.to_lowercase().contains("graphiql"),
        "expected the GraphiQL page, got: {playground_html}"
    );

    // Introspection — GraphiQL's "Docs" panel (and any other GraphQL client) relies on this to
    // discover the schema; confirms it isn't disabled.
    let introspection = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({ "query": "{ __schema { queryType { name } } }" }))
        .send()
        .await
        .unwrap();
    assert_eq!(introspection.status(), 200);
    let introspected: serde_json::Value = introspection.json().await.unwrap();
    assert!(
        introspected.get("errors").is_none(),
        "unexpected GraphQL errors: {introspected:?}"
    );
    assert_eq!(introspected["data"]["__schema"]["queryType"]["name"], "Query");

    // create mutation
    let create_res = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": "mutation($data: Json!) { createTestGqlHttpOrders(data: $data) { id name } }",
            "variables": { "data": { "name": "First" } },
        }))
        .send()
        .await
        .unwrap();
    assert_eq!(create_res.status(), 200);
    let created: serde_json::Value = create_res.json().await.unwrap();
    assert!(
        created.get("errors").is_none(),
        "unexpected GraphQL errors: {created:?}"
    );
    let id = created["data"]["createTestGqlHttpOrders"]["id"]
        .as_str()
        .unwrap()
        .to_string();
    assert_eq!(created["data"]["createTestGqlHttpOrders"]["name"], "First");

    // get query
    let get_res = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(r#"{{ testGqlHttpOrders(id: "{id}") {{ id name }} }}"#),
        }))
        .send()
        .await
        .unwrap();
    assert_eq!(get_res.status(), 200);
    let fetched: serde_json::Value = get_res.json().await.unwrap();
    assert!(
        fetched.get("errors").is_none(),
        "unexpected GraphQL errors: {fetched:?}"
    );
    assert_eq!(fetched["data"]["testGqlHttpOrders"]["id"], id);

    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}
