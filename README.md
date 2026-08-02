# Metap

Metap is a metadata-driven platform core.

Chosen stack:

- Fastify for the HTTP runtime.
- Zod for request/config validation.
- Drizzle ORM for PostgreSQL access.
- PostgreSQL as the system of record.
- RabbitMQ for integration events.
- Outbox Pattern for reliable event publishing.

This is a pnpm workspace: `packages/core` is the entity-agnostic platform library, `apps/crm` is the one business module (the only thing that actually runs), `packages/platform-react` + `apps/demo` are the frontend equivalent. Every command below runs from the repo root.

Start locally:

```bash
pnpm install
cp packages/core/.env.example packages/core/.env
cp apps/crm/.env.example apps/crm/.env
docker compose up -d postgres rabbitmq
pnpm db:generate
pnpm db:migrate
pnpm dev
```

Quality commands:

```bash
pnpm lint
pnpm lint:fix
pnpm format:check
pnpm format
pnpm typecheck
```

Docs:

- [Architecture](docs/architectures/index.md)
- [Why This Stack](docs/why.md)
- [Roadmap](docs/roadmap.md)
