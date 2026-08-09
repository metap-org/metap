# Binary name is fixed as `app` (see Cargo.toml's `[[bin]]`) regardless of what this
# project is named, so nothing here needs templating.
#
# No secrets are baked in: DATABASE_URL/RABBITMQ_URL/AUTH_JWT_PUBLIC_KEY_PATH etc. are read
# from the environment at container start, same as the local `.env` convention, just
# supplied by the orchestrator instead of a checked-in file. The JWT public key must be
# mounted at runtime (e.g. `-v ./keys:/app/keys:ro`) — see the ENV note near the bottom.

# ---- build -----------------------------------------------------------------
FROM rust:1-slim-bookworm AS build
WORKDIR /workspace
COPY Cargo.toml Cargo.lock ./
COPY src src
RUN cargo build --release --locked

# ---- runtime -----------------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 app \
    && useradd --system --uid 10001 --gid app --home-dir /app --shell /usr/sbin/nologin app

WORKDIR /app
COPY --from=build /workspace/target/release/app ./app
RUN chown -R app:app /app

ENV HOST=0.0.0.0
ENV PORT=3000
# No default for AUTH_JWT_PUBLIC_KEY_PATH on purpose: `metap::load_config()` already fails
# fast on a missing required env var, which is safer than a default path that silently
# resolves to nothing. Mount the real key and point this at it, e.g.:
#   docker run -v ./keys:/app/keys:ro -e AUTH_JWT_PUBLIC_KEY_PATH=/app/keys/jwt-public.pem ...
EXPOSE 3000

USER app
ENTRYPOINT ["./app"]
