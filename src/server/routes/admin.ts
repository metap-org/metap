import { z } from "zod";
import type { FastifyInstance, FastifyRequest } from "fastify";
import type { AppContainer } from "../../core/container";
import type { PolicyCondition } from "../../core/permission/policy-condition";
import type { RequestContext } from "../../core/permission/permission-service";
import { sendServiceError } from "../error-handler";

const UserIdParamsSchema = z.object({ userId: z.string().uuid() });
const RoleParamsSchema = z.object({ userId: z.string().uuid(), role: z.string().min(1) });
const AssignRoleBodySchema = z.object({ role: z.string().min(1) });

const PolicyValueSchema = z.union([
  z.object({ literal: z.unknown() }),
  z.object({ fromContext: z.enum(["tenantId", "userId", "roles", "functionId"]) }),
]);

const PolicyConditionSchema: z.ZodType<PolicyCondition> = z.lazy(() =>
  z.union([
    z.object({
      attribute: z.string(),
      op: z.enum(["eq", "neq", "in", "notIn"]),
      value: PolicyValueSchema,
    }),
    z.object({ all: z.array(PolicyConditionSchema) }),
    z.object({ any: z.array(PolicyConditionSchema) }),
  ]),
);

const CreatePolicyBodySchema = z.object({
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update", "write"]),
  roles: z.array(z.string()).optional(),
  condition: PolicyConditionSchema.optional(),
  field: z.string().optional(),
  subject: z.enum(["context", "record"]).optional(),
});

const PolicyIdParamsSchema = z.object({ id: z.string().uuid() });
const ListPoliciesQuerySchema = z.object({ entity: z.string().optional() });

const ExplainBodySchema = z.object({
  roles: z.array(z.string()),
  entity: z.string().min(1),
  action: z.enum(["read", "create", "update", "write"]),
  field: z.string().optional(),
  record: z.record(z.string(), z.unknown()).optional(),
});

function isAdmin(request: FastifyRequest) {
  return request.context.roles?.includes("admin") ?? false;
}

export function registerAdminRoutes(app: FastifyInstance, container: AppContainer) {
  app.get("/admin/users", async (request, reply) => {
    if (!isAdmin(request)) {
      return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
    }

    const users = await container.roleAssignments.listUsers(request.context.tenantId);
    return { data: users };
  });

  app.get<{ Params: { userId: string } }>(
    "/admin/users/:userId/roles",
    { schema: { params: z.toJSONSchema(UserIdParamsSchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = UserIdParamsSchema.parse(request.params);
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return { data: roles };
    },
  );

  app.post<{ Params: { userId: string }; Body: z.infer<typeof AssignRoleBodySchema> }>(
    "/admin/users/:userId/roles",
    {
      schema: {
        params: z.toJSONSchema(UserIdParamsSchema, { target: "draft-7" }),
        body: z.toJSONSchema(AssignRoleBodySchema, { target: "draft-7" }),
      },
    },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = UserIdParamsSchema.parse(request.params);
      const body = AssignRoleBodySchema.parse(request.body);
      await container.roleAssignments.assignRole(
        request.context.tenantId,
        params.userId,
        body.role,
        request.context.userId,
      );
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return reply.code(201).send({ data: roles });
    },
  );

  app.delete<{ Params: { userId: string; role: string } }>(
    "/admin/users/:userId/roles/:role",
    { schema: { params: z.toJSONSchema(RoleParamsSchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = RoleParamsSchema.parse(request.params);
      await container.roleAssignments.revokeRole(
        request.context.tenantId,
        params.userId,
        params.role,
      );
      const roles = await container.roleAssignments.getRolesForUser(
        request.context.tenantId,
        params.userId,
      );
      return { data: roles };
    },
  );

  app.get<{ Querystring: { entity?: string } }>(
    "/admin/policies",
    { schema: { querystring: z.toJSONSchema(ListPoliciesQuerySchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const query = ListPoliciesQuerySchema.parse(request.query);
      const rows = await container.permissions.listPolicies(request.context.tenantId, query.entity);
      return { data: rows };
    },
  );

  app.post<{ Body: z.infer<typeof CreatePolicyBodySchema> }>(
    "/admin/policies",
    { schema: { body: z.toJSONSchema(CreatePolicyBodySchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const body = CreatePolicyBodySchema.parse(request.body);
      const created = await container.permissions.createPolicy(
        request.context.tenantId,
        body.entity,
        body.action,
        body.roles,
        body.condition,
        request.context.userId,
        body.field,
        body.subject,
      );
      return reply.code(201).send({ data: created });
    },
  );

  app.delete<{ Params: { id: string } }>(
    "/admin/policies/:id",
    { schema: { params: z.toJSONSchema(PolicyIdParamsSchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const params = PolicyIdParamsSchema.parse(request.params);
      await container.permissions.deletePolicy(request.context.tenantId, params.id);
      return { data: null };
    },
  );

  app.post<{ Body: z.infer<typeof ExplainBodySchema> }>(
    "/admin/policies/explain",
    { schema: { body: z.toJSONSchema(ExplainBodySchema, { target: "draft-7" }) } },
    async (request, reply) => {
      if (!isAdmin(request)) {
        return sendServiceError(request, reply, { ok: false, status: 403, error: "forbidden" });
      }

      const body = ExplainBodySchema.parse(request.body);
      const simulatedContext: RequestContext = {
        tenantId: request.context.tenantId,
        roles: body.roles,
      };
      const explanation = await container.permissions.explain(
        simulatedContext,
        body.entity,
        body.action,
        { field: body.field, record: body.record },
      );
      return { data: explanation };
    },
  );
}
