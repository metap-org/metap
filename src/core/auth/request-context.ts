import type { RequestContext } from "../permission/permission-service";
import type { Claims } from "./jwt-verifier";

export function buildRequestContext(claims: Claims, roles: readonly string[]): RequestContext {
  const context: RequestContext = {
    tenantId: claims.tenantId,
    userId: claims.sub,
    roles,
  };

  if (claims.functionId !== undefined) {
    context.functionId = claims.functionId;
  }

  return context;
}
