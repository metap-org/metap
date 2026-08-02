import { evaluatePolicyRow } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";
import { explainPolicies } from "./policy-explainer";
import type { PolicyExplanation } from "./policy-explainer";
import { PermissionSnapshot } from "./permission-snapshot";
import type { PolicyStore } from "./policy-store";

export type { PolicyRow } from "./policy-store";

export type RequestContext = {
  tenantId: string;
  userId?: string;
  roles?: readonly string[];
  functionId?: string;
};

export type PermissionDecision = {
  allowed: boolean;
  reason?: string;
  field?: string;
};

export type EntityAction = "read" | "create" | "update" | "delete";

export class PermissionService {
  constructor(private readonly store: PolicyStore) {}

  private async checkAction(
    context: RequestContext,
    entityName: string,
    action: EntityAction,
  ): Promise<PermissionDecision> {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const rows = await this.store.findContextPolicies(context.tenantId, entityName, action);

    if (rows.length === 0) {
      return { allowed: true };
    }

    const passed = rows.some((policy) => evaluatePolicyRow(policy, context, undefined));

    return passed ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }

  canReadEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "read");
  }

  canCreateEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "create");
  }

  canUpdateEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "update");
  }

  canDeleteEntity(context: RequestContext, entity: string): Promise<PermissionDecision> {
    return this.checkAction(context, entity, "delete");
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }

  async loadSnapshot(tenantId: string, entity: string): Promise<PermissionSnapshot> {
    return PermissionSnapshot.load(this.store, tenantId, entity);
  }

  async listPolicies(tenantId: string, entity?: string) {
    return this.store.listPolicies(tenantId, entity);
  }

  async createPolicy(
    tenantId: string,
    entity: string,
    action: string,
    roles: string[] | undefined,
    condition: PolicyCondition | undefined,
    createdBy: string | undefined,
    field?: string,
    subject?: "context" | "record",
  ) {
    return this.store.createPolicy(
      tenantId,
      entity,
      action,
      roles,
      condition,
      createdBy,
      field,
      subject,
    );
  }

  async deletePolicy(tenantId: string, id: string): Promise<void> {
    await this.store.deletePolicy(tenantId, id);
  }

  async explain(
    context: RequestContext,
    entity: string,
    action: string,
    options?: { field?: string | undefined; record?: Record<string, unknown> | undefined },
  ): Promise<PolicyExplanation> {
    const rows = await this.store.findExplainPolicies(context.tenantId, entity, action, {
      ...(options?.field ? { field: options.field } : {}),
      ...(!options?.field && options?.record ? { subject: "record" as const } : {}),
    });

    return explainPolicies(rows, context, options?.record);
  }
}
