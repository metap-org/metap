import type { MetadataRegistry } from "../metadata/metadata-registry";

export type RequestContext = {
  tenantId: string;
  userId?: string;
  roles?: readonly string[];
  functionId?: string;
};

export type PermissionDecision = {
  allowed: boolean;
  reason?: string;
};

type EntityAction = "read" | "create" | "update";

export class PermissionService {
  constructor(private readonly metadata: MetadataRegistry) {}

  private checkAction(
    context: RequestContext,
    entityName: string,
    action: EntityAction,
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const entity = this.metadata.getEntity(entityName);
    const allowedRoles = entity?.permissions?.[action];

    if (!allowedRoles) {
      return { allowed: true };
    }

    const callerRoles = context.roles ?? [];
    const hasAllowedRole = callerRoles.some((role) => allowedRoles.includes(role));

    return hasAllowedRole ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }

  canReadEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "read");
  }

  canCreateEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "create");
  }

  canUpdateEntity(context: RequestContext, entity: string): PermissionDecision {
    return this.checkAction(context, entity, "update");
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }
}
