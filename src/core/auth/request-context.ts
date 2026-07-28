import type { RequestContext } from "../permission/permission-service";
import type { Claims } from "./jwt-verifier";

export function buildRequestContext(claims: Claims): RequestContext {
  const context: RequestContext = {
    tenantId: claims.tenantId,
    userId: claims.sub,
    roles: claims.roles,
  };

  if (claims.functionId !== undefined) {
    context.functionId = claims.functionId;
  }

  return context;
}
