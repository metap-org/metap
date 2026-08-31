//! `TcpListener::bind` + `axum::serve(...).with_graceful_shutdown(shutdown::signal())` +
//! the "listening" log line — copy-pasted with small variations at the end of every binary's
//! `main.rs` that serves HTTP (`metap-demo-crm`, `metap-demo-jira`, `templates/metap-app`,
//! `graphql-gateway`).

use std::convert::Infallible;

use axum::extract::Request;
use axum::response::Response;
use axum::serve::IncomingStream;
use tokio::net::TcpListener;
use tower::Service;

pub async fn run<M, S>(addr: &str, make_service: M) -> anyhow::Result<()>
where
    M: for<'a> Service<IncomingStream<'a, TcpListener>, Error = Infallible, Response = S> + Send + 'static,
    for<'a> <M as Service<IncomingStream<'a, TcpListener>>>::Future: Send,
    S: Service<Request, Response = Response, Error = Infallible> + Clone + Send + 'static,
    S::Future: Send,
{
    let listener = TcpListener::bind(addr).await?;
    tracing::info!(%addr, "listening");
    axum::serve(listener, make_service)
        .with_graceful_shutdown(crate::shutdown::signal())
        .await?;
    Ok(())
}
