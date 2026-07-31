import type { FastifyInstance } from "fastify";
import type { AppContainer } from "../../core/container";
import { sendServiceError } from "../error-handler";

export function registerMetadataRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/metadata/entities", async () => ({
    data: container.metadata.listEntities(),
  }));

  app.get<{ Params: { entity: string } }>("/metadata/entities/:entity", async (request, reply) => {
    const entity = container.metadata.getEntityMetadata(request.params.entity);

    if (!entity) {
      return sendServiceError(request, reply, {
        ok: false,
        status: 404,
        error: "entity_not_found",
      });
    }

    return { data: entity };
  });
}
