import type { FastifyInstance } from "fastify";
import type { AppContainer } from "../../core/container";

export function registerHealthRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/health", async () => {
    const db = await container.health.checkDatabase();

    return {
      status: db ? "ok" : "degraded",
      checks: {
        database: db,
      },
    };
  });
}
