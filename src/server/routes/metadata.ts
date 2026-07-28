import type { FastifyInstance } from "fastify";
import type { AppContainer } from "../../core/container";

export function registerMetadataRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/metadata/entities", async () => ({
    data: container.metadata.listEntities(),
  }));

  app.get<{ Params: { entity: string } }>("/metadata/entities/:entity", async (request, reply) => {
    const entity = container.metadata.getEntity(request.params.entity);

    if (!entity) {
      return reply.code(404).send({ error: "entity_not_found" });
    }

    return { data: entity };
  });
}
