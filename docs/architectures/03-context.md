# 3. System Scope and Context

## Business Context

| Actor | Interaction |
|---|---|
| End User | Uses a business app built on Metap (CRM today) — creates/reads/updates records, lists/filters/searches, runs workflow transitions |
| Admin | Grants/revokes roles per user, manages field- and record-level permission policies, uses the read-only policy simulator |

Out of scope today: no external system integrations exist (no payment gateway, no email/notification provider, no third-party identity provider beyond verifying externally-issued JWTs). Metap is verify-only for auth — it does not issue tokens itself in production (a dev-only `pnpm mint-token` script exists for local testing).

## C4 Level 1: System Context

```mermaid
C4Context
  title System Context — Metap

  Person(user, "End User", "Uses a business app built on Metap (CRM today; ERP/other modules later)")
  Person(admin, "Admin", "Manages roles and permission policies")

  System(metap, "Metap", "Metadata-driven platform core: CRUD, permission, query planning, workflow, outbox")

  Rel(user, metap, "Uses", "HTTPS/JSON, JWT")
  Rel(admin, metap, "Administers roles & policies", "HTTPS/JSON, JWT")
```

Metap has no external system integrations yet (no payment/email/notification providers) — the only actors today are end users and admins of whatever business app is built on top of it.

## Technical Context

- **Protocol**: REST over HTTPS, JSON bodies, `Authorization: Bearer <JWT>`.
- **Auth**: Metap verifies externally-issued JWTs (RS256, public key configured via `authJwtPublicKeyPath`) — it does not mint tokens for production use. Roles are *not* carried in the JWT; they're looked up fresh per request from `user_roles` (see [05. Building Block View](05-building-blocks.md)).
- **Errors**: structured JSON error bodies with a request id and trace id (`src/server/error-handler.ts`).
- **Events out**: RabbitMQ, AMQP 0-9-1, via the transactional outbox — no synchronous webhook/callback mechanism exists.
