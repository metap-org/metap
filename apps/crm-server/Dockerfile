# apps/crm-server's own Dockerfile — colocated with it deliberately, same as its keys/ and
# .env (see CLAUDE.md's Monorepo Layout: "apps/ holds sample/example consumers... kept
# together specifically so the 'this is a throwaway example, not the product' signal lives
# in one place"). This packages *this example app specifically*, not "the repo" — a
# downstream project builds its own equivalent of crm-server (its own main.rs registering
# its own entities) and should write its own analogous Dockerfile for it, the same way it
# writes its own main.rs rather than importing this one. Treat this file as a reference to
# copy and adapt, not infrastructure to depend on.
#
# The build context is still the repo root (`docker build -f apps/crm-server/Dockerfile .`
# from the repo root) because the Rust/pnpm workspaces both live there — Cargo needs every
# workspace member's manifest (see the backend-build stage below) and pnpm needs
# packages/platform-react, which crm-fe consumes via workspace:*.
#
# Builds crm-server plus apps/crm-fe's static build, served from the same process/port the
# way `pnpm start` does locally (`STATIC_DIR`, see `apps/crm-server/.env.example`). Phase 8
# Hardening ("non-root Docker image", `docs/roadmap.md`) — the first Dockerfile in the repo.
#
# No secrets are baked in: DATABASE_URL/RABBITMQ_URL/AUTH_JWT_PUBLIC_KEY_PATH etc. are read
# from the environment at container start (`metap-infra/src/config.rs`), same as the local
# `.env` convention, just supplied by the orchestrator instead of a checked-in file. The JWT
# public key must be mounted at runtime (e.g. `-v ./keys:/app/keys:ro`) — see the ENV note
# near the bottom.

# ---- frontend build ---------------------------------------------------------
FROM node:24-slim AS frontend-build
WORKDIR /workspace
RUN corepack enable
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY packages/platform-react packages/platform-react
COPY apps/crm-fe apps/crm-fe
RUN pnpm install --frozen-lockfile
RUN pnpm --filter @metap/crm-fe build

# ---- backend build ------------------------------------------------------------
FROM rust:1-slim-bookworm AS backend-build
WORKDIR /workspace
# The root Cargo.toml's `[workspace] members` list every crate under `crates/` plus
# `apps/crm-server` (see CLAUDE.md's Monorepo Layout) — Cargo needs all of their manifests
# present to resolve the workspace even though `--locked -p crm-server` only compiles
# crm-server's own dependency graph, not every member.
COPY Cargo.toml Cargo.lock ./
COPY crates crates
COPY apps/crm-server apps/crm-server
RUN cargo build --release --locked -p crm-server

# ---- runtime --------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 metap \
    && useradd --system --uid 10001 --gid metap --home-dir /app --shell /usr/sbin/nologin metap

WORKDIR /app
COPY --from=backend-build /workspace/target/release/crm-server ./crm-server
COPY --from=frontend-build /workspace/apps/crm-fe/dist ./static
RUN chown -R metap:metap /app

ENV HOST=0.0.0.0
ENV PORT=3000
ENV STATIC_DIR=/app/static
# No default for AUTH_JWT_PUBLIC_KEY_PATH on purpose: `metap-infra/src/config.rs` already
# fails fast on a missing required env var, which is safer than a default path that
# silently resolves to nothing. Mount the real key and point this at it, e.g.:
#   docker run -v ./keys:/app/keys:ro -e AUTH_JWT_PUBLIC_KEY_PATH=/app/keys/jwt-public.pem ...
EXPOSE 3000

USER metap
ENTRYPOINT ["./crm-server"]
