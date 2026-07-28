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

export class PermissionService {
  canReadEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  canCreateEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  canUpdateEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }
}
