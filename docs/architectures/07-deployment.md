# 7. Deployment View

Deployment topology for local development (`docker compose` + two Node processes). Production isn't built out yet ([Phase 8: Hardening](../roadmap.md) is not started) — this reflects today's actual dev setup, not a target production topology. (Kruchten 4+1's Physical View.)

```mermaid
graph TB
  subgraph compose["docker compose"]
    PG[("PostgreSQL 16<br/>host :5433 -> 5432")]
    MQ[["RabbitMQ<br/>:5672 AMQP, :15672 mgmt UI"]]
  end

  subgraph node["Node.js processes (pnpm)"]
    API["API Server<br/>pnpm dev / node dist/main.js<br/>:3000"]
    Worker["Outbox Publisher<br/>pnpm worker:outbox"]
  end

  subgraph vite["Vite dev server"]
    Web["Web Frontend<br/>:5173, proxies /api /metadata /health"]
  end

  Web --> API
  API --> PG
  API --> MQ
  Worker --> PG
  Worker --> MQ
```

## Notes

- API Server and Outbox Publisher are separate `node` processes today, not separate containers — either can be containerized independently without code changes, since they already only communicate through PostgreSQL/RabbitMQ.
- No production deployment topology is documented yet — no orchestrator (Kubernetes, ECS, etc.), no load balancer, no autoscaling, no secrets manager. This is real, tracked debt — see [11. Risks and Technical Debt](11-risks.md).
- `docker compose` here is a local dev convenience, not a deployment target — `docker-compose.yml` only runs `postgres` and `rabbitmq`; the API/worker/frontend all run as plain `pnpm`/`node` processes on the host.
