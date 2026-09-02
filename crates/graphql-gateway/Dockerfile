# Unlike a downstream binary's own Dockerfile (`../../templates/metap-app/Dockerfile`,
# `../../../metap-demo-crm/Dockerfile`) — this one IS buildable today. `graphql-gateway` is a
# binary inside `metap`'s own Cargo workspace, not a downstream repo depending on `metap` via a
# `path = "../metap/..."` dependency that can't cross the build context boundary. Build context
# is this repo's root (`metap/`), not this crate's own directory — e.g. from a downstream repo
# that wants its own gateway instance (`../metap-demo-waf/data-plane/docker-compose.yml`):
#   docker build -f ../../metap/crates/graphql-gateway/Dockerfile -t waf-graphql-gateway ../../metap
#
# No secrets are baked in: `UPSTREAM_<N>_SERVICE_EMAIL`/`UPSTREAM_<N>_SERVICE_PASSWORD`/
# `AUTH_JWT_PUBLIC_KEY_PATH` etc. are read from the environment at container start. The gateway's
# own decode-only public key must be mounted at runtime (e.g. `-v ./keys:/app/keys:ro`) — see the
# ENV note near the bottom.

# ---- build -----------------------------------------------------------------
FROM rust:1-slim-bookworm AS build
WORKDIR /workspace
# Whole workspace, not just this crate — `graphql-gateway` depends on sibling crates
# (`metap-graphql`, `metap-grpc`, `metap-runtime`, ...) that `cargo build -p` still needs on
# disk to resolve, same reason `metap`'s own root `cargo build --workspace` always runs from
# the repo root. `.dockerignore` (repo root) already excludes `target`/`.git`/`node_modules`.
COPY Cargo.toml Cargo.lock ./
COPY crates crates
RUN cargo build --release --locked -p metap-graphql-gateway

# ---- runtime -----------------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 app \
    && useradd --system --uid 10001 --gid app --home-dir /app --shell /usr/sbin/nologin app

WORKDIR /app
COPY --from=build /workspace/target/release/graphql-gateway ./graphql-gateway
RUN chown -R app:app /app

ENV HOST=0.0.0.0
ENV PORT=4000
# No default for AUTH_JWT_PUBLIC_KEY_PATH on purpose — fail fast on a missing required env var
# rather than silently resolving to nothing. Mount the real key and point this at it, e.g.:
#   docker run -v ./keys:/app/keys:ro -e AUTH_JWT_PUBLIC_KEY_PATH=/app/keys/jwt-public.pem ...
EXPOSE 4000

USER app
ENTRYPOINT ["./graphql-gateway"]
