# Metap

Metap is a metadata-driven platform core.

Chosen stack:

- Fastify for the HTTP runtime.
- Zod for request/config validation.
- Drizzle ORM for PostgreSQL access.
- PostgreSQL as the system of record.
- RabbitMQ for integration events.
- Outbox Pattern for reliable event publishing.

Start locally:

```bash
pnpm install
cp .env.example .env
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

- [Architecture](docs/architecture.md)
- [Why This Stack](docs/why.md)
- [Roadmap](docs/roadmap.md)
