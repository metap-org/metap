import type { FastifyInstance } from "fastify";
import type { AppContainer } from "../../core/container";
import { generateOpenApiDocument } from "../../core/metadata/openapi-generator";
import { sendServiceError } from "../error-handler";

// Public: describes API shape only (entity/field names, kinds, workflow
// structure) — no tenant data, no records, comparable to any public
// OpenAPI/Swagger doc. Fetchable without a token so codegen tooling
// (openapi-typescript) can point straight at a running server.
export function registerOpenApiRoute(app: FastifyInstance, container: AppContainer) {
  app.get("/metadata/openapi.json", () =>
    generateOpenApiDocument(container.metadata.listEntities()),
  );
}

export function registerMetadataRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/metadata/entities", () => ({
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
