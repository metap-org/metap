import type { FastifyInstance } from "fastify";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import { AuthError } from "../../core/auth/errors";
import type { RequestContext } from "../../core/permission/permission-service";
import { buildRequestContext } from "../../core/auth/request-context";
import type { RoleAssignmentService } from "../../core/auth/role-assignment-service";

declare module "fastify" {
  interface FastifyRequest {
    context: RequestContext;
  }
}

const BEARER_PREFIX = "Bearer ";

export function registerAuthHook(
  instance: FastifyInstance,
  verifier: JwtVerifier,
  roleAssignments: Pick<RoleAssignmentService, "getRolesForUser">,
) {
  instance.decorateRequest("context", null, []);

  instance.addHook("onRequest", async (request) => {
    const header = request.headers.authorization;

    if (!header || !header.startsWith(BEARER_PREFIX)) {
      throw new AuthError("Missing or invalid authorization header.");
    }

    const token = header.slice(BEARER_PREFIX.length);
    const claims = verifier.verify(token);
    const roles = await roleAssignments.getRolesForUser(claims.tenantId, claims.sub);
    request.context = buildRequestContext(claims, roles);
  });
}
