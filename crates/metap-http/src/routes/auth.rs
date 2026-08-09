//! Public `POST /auth/login` — the first route in this crate that mints a JWT instead of only
//! verifying one. See `metap_peripherals::auth`'s doc comment for why the encoding logic lives
//! there (shared with `dev-tools mint-token`), not here.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;

use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

const TOKEN_TTL_SECONDS: u64 = 3600;

#[derive(Deserialize)]
struct LoginBody {
    email: String,
    password: String,
}

async fn login(State(state): State<AppState>, Json(body): Json<LoginBody>) -> Response {
    let user =
        match metap_peripherals::verify_credentials(&state.pool, &body.email, &body.password).await {
            Ok(Some(user)) => user,
            Ok(None) => {
                return service_error_response(
                    401,
                    "invalid_credentials",
                    Some("Invalid email or password."),
                    None,
                )
            }
            Err(e) => return internal_error_response(e),
        };

    match metap_peripherals::mint_jwt(
        &state.jwt_encoding_key_pem,
        user.tenant_id,
        user.id,
        TOKEN_TTL_SECONDS,
    ) {
        Ok(token) => Json(json!({ "data": { "token": token } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new().route("/auth/login", post(login))
}
