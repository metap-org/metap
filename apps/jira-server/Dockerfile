# apps/jira-server's own Dockerfile — same reasoning as apps/crm-server/Dockerfile's doc
# comment: colocated deliberately, packages this example app specifically, treat as a
# reference to copy/adapt rather than infrastructure to depend on. Backend-only (no frontend
# build stage) — this PoC has no frontend.
#
# Build context is still the repo root (`docker build -f apps/jira-server/Dockerfile .`) since
# the Cargo workspace lives there.
#
# No secrets baked in — DATABASE_URL/RABBITMQ_URL/AUTH_JWT_PUBLIC_KEY_PATH etc. read from the
# environment at container start, same as the local `.env` convention. The JWT public key must
# be mounted at runtime (e.g. `-v ./keys:/app/keys:ro`).

FROM rust:1-slim-bookworm AS backend-build
WORKDIR /workspace
# Every workspace member's manifest must be present for Cargo to resolve the workspace, even
# though `-p jira-server` only compiles this app's own dependency graph — `COPY apps apps`
# (whole directory), not one line per app, so this doesn't need editing again the next time a
# new app joins `apps/` (see `apps/crm-server/Dockerfile`'s matching comment).
COPY Cargo.toml Cargo.lock ./
COPY crates crates
COPY apps apps
RUN cargo build --release --locked -p jira-server

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 metap \
    && useradd --system --uid 10001 --gid metap --home-dir /app --shell /usr/sbin/nologin metap

WORKDIR /app
COPY --from=backend-build /workspace/target/release/jira-server ./jira-server
RUN chown -R metap:metap /app

ENV HOST=0.0.0.0
ENV PORT=3100
# No default for AUTH_JWT_PUBLIC_KEY_PATH on purpose — metap-infra/src/config.rs fails fast on
# a missing required env var, safer than a default path that silently resolves to nothing.
EXPOSE 3100

USER metap
ENTRYPOINT ["./jira-server"]
