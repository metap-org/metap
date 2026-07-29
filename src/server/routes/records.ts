import { z } from "zod";
import type { FastifyInstance } from "fastify";
import { zodToJsonSchema } from "zod-to-json-schema";
import type { AppContainer } from "../../core/container";
import { sendServiceError } from "../error-handler";

const ListQuerySchema = z.object({
  limit: z.coerce.number().int().positive().max(200).default(30),
  cursor: z.string().optional(),
  sort: z.string().optional(),
});

const RecordBodySchema = z.object({
  data: z.record(z.unknown()),
});

const UpdateBodySchema = z.object({
  version: z.number().int().positive(),
  data: z.record(z.unknown()),
});

const UpdateParamsSchema = z.object({ entity: z.string(), id: z.string().uuid() });

export function registerRecordRoutes(app: FastifyInstance, container: AppContainer) {
  app.get<{ Params: { entity: string }; Querystring: z.infer<typeof ListQuerySchema> }>(
    "/api/:entity",
    {
      schema: {
        querystring: zodToJsonSchema(ListQuerySchema),
      },
    },
    async (request, reply) => {
      const query = ListQuerySchema.parse(request.query);
      const result = await container.crud.list(request.params.entity, query, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data, page: result.page };
    },
  );

  app.post<{ Params: { entity: string }; Body: z.infer<typeof RecordBodySchema> }>(
    "/api/:entity",
    {
      schema: {
        body: zodToJsonSchema(RecordBodySchema),
      },
    },
    async (request, reply) => {
      const body = RecordBodySchema.parse(request.body);
      const result = await container.crud.create(request.params.entity, body.data, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return reply.code(201).send({ data: result.data });
    },
  );

  app.patch<{
    Params: { entity: string; id: string };
    Body: z.infer<typeof UpdateBodySchema>;
  }>(
    "/api/:entity/:id",
    {
      schema: {
        params: zodToJsonSchema(UpdateParamsSchema),
        body: zodToJsonSchema(UpdateBodySchema),
      },
    },
    async (request, reply) => {
      const params = UpdateParamsSchema.parse(request.params);
      const body = UpdateBodySchema.parse(request.body);
      const result = await container.crud.update(
        params.entity,
        params.id,
        body.version,
        body.data,
        request.context,
      );

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );
}
