import { z } from "zod";
import type { FastifyInstance } from "fastify";
import type { AppContainer } from "../../core/container";
import { sendServiceError } from "../error-handler";
import type { ListInput } from "../../core/query/query-planner";

const ListQuerySchema = z.object({
  limit: z.coerce.number().int().positive().max(200).default(30),
  sort: z.string().optional(),
  cursor: z.string().optional(),
});

const reservedKeys = new Set(Object.keys(ListQuerySchema.shape));

const RecordBodySchema = z.object({
  data: z.record(z.string(), z.unknown()),
});

const UpdateBodySchema = z.object({
  version: z.number().int().positive(),
  data: z.record(z.string(), z.unknown()),
});

const UpdateParamsSchema = z.object({ entity: z.string(), id: z.string().uuid() });

const DeleteBodySchema = z.object({
  version: z.number().int().positive(),
});

const TransitionBodySchema = z.object({
  version: z.number().int().positive(),
});

const TransitionParamsSchema = z.object({
  entity: z.string(),
  id: z.string().uuid(),
  action: z.string(),
});

export function registerRecordRoutes(app: FastifyInstance, container: AppContainer) {
  app.get<{ Params: { entity: string }; Querystring: Record<string, unknown> }>(
    "/api/:entity",
    async (request, reply) => {
      const query = ListQuerySchema.parse(request.query);
      const filters: Record<string, string> = {};

      for (const [key, value] of Object.entries(request.query)) {
        if (reservedKeys.has(key)) {
          continue;
        }

        if (typeof value === "string") {
          filters[key] = value;
        }
      }

      const listInput: ListInput = { limit: query.limit, filters };

      if (query.sort !== undefined) {
        listInput.sort = query.sort;
      }

      if (query.cursor !== undefined) {
        listInput.cursor = query.cursor;
      }

      const result = await container.crud.list(request.params.entity, listInput, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data, page: result.page };
    },
  );

  app.get<{ Params: { entity: string; id: string } }>(
    "/api/:entity/:id",
    {
      schema: {
        params: z.toJSONSchema(UpdateParamsSchema, { target: "draft-7" }),
      },
    },
    async (request, reply) => {
      const params = UpdateParamsSchema.parse(request.params);
      const result = await container.crud.get(params.entity, params.id, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );

  app.post<{ Params: { entity: string }; Body: z.infer<typeof RecordBodySchema> }>(
    "/api/:entity",
    {
      schema: {
        body: z.toJSONSchema(RecordBodySchema, { target: "draft-7" }),
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
        params: z.toJSONSchema(UpdateParamsSchema, { target: "draft-7" }),
        body: z.toJSONSchema(UpdateBodySchema, { target: "draft-7" }),
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

  app.delete<{
    Params: { entity: string; id: string };
    Body: z.infer<typeof DeleteBodySchema>;
  }>(
    "/api/:entity/:id",
    {
      schema: {
        params: z.toJSONSchema(UpdateParamsSchema, { target: "draft-7" }),
        body: z.toJSONSchema(DeleteBodySchema, { target: "draft-7" }),
      },
    },
    async (request, reply) => {
      const params = UpdateParamsSchema.parse(request.params);
      const body = DeleteBodySchema.parse(request.body);
      const result = await container.crud.delete(
        params.entity,
        params.id,
        body.version,
        request.context,
      );

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );

  app.post<{
    Params: { entity: string; id: string; action: string };
    Body: z.infer<typeof TransitionBodySchema>;
  }>(
    "/api/:entity/:id/transitions/:action",
    {
      schema: {
        params: z.toJSONSchema(TransitionParamsSchema, { target: "draft-7" }),
        body: z.toJSONSchema(TransitionBodySchema, { target: "draft-7" }),
      },
    },
    async (request, reply) => {
      const params = TransitionParamsSchema.parse(request.params);
      const body = TransitionBodySchema.parse(request.body);
      const result = await container.crud.transition(
        params.entity,
        params.id,
        params.action,
        body.version,
        request.context,
      );

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );
}
