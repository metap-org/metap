import { and, eq, isNull } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluatePolicyRow } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";
import { explainPolicies } from "./policy-explainer";
import type { PolicyExplanation } from "./policy-explainer";
import { PermissionSnapshot } from "./permission-snapshot";

export type PolicyRow = typeof policies.$inferSelect;

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

export type EntityAction = "read" | "create" | "update";

export class PermissionService {
  constructor(private readonly db: Database) {}

  private async checkAction(
    context: RequestContext,
    entityName: string,
    action: EntityAction,
  ): Promise<PermissionDecision> {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const rows = await this.db.client
      .select()
      .from(policies)
      .where(
        and(
          eq(policies.tenantId, context.tenantId),
          eq(policies.entity, entityName),
          eq(policies.action, action),
          isNull(policies.field),
          eq(policies.subject, "context"),
        ),
      );

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

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }

  async loadSnapshot(tenantId: string, entity: string): Promise<PermissionSnapshot> {
    return PermissionSnapshot.load(this.db, tenantId, entity);
  }

  async listPolicies(tenantId: string, entity?: string) {
    const where = entity
      ? and(eq(policies.tenantId, tenantId), eq(policies.entity, entity))
      : eq(policies.tenantId, tenantId);

    return this.db.client.select().from(policies).where(where);
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
    const inserted = await this.db.client
      .insert(policies)
      .values({
        tenantId,
        entity,
        action,
        roles: roles ?? null,
        condition: condition ?? null,
        createdBy,
        field: field ?? null,
        subject: subject ?? "context",
      })
      .returning();

    return inserted[0];
  }

  async deletePolicy(tenantId: string, id: string): Promise<void> {
    await this.db.client
      .delete(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.id, id)));
  }

  async explain(
    context: RequestContext,
    entity: string,
    action: string,
    options?: { field?: string | undefined; record?: Record<string, unknown> | undefined },
  ): Promise<PolicyExplanation> {
    const base = [
      eq(policies.tenantId, context.tenantId),
      eq(policies.entity, entity),
      eq(policies.action, action),
    ];

    const where = options?.field
      ? and(...base, eq(policies.field, options.field))
      : options?.record
        ? and(...base, isNull(policies.field), eq(policies.subject, "record"))
        : and(...base, isNull(policies.field), eq(policies.subject, "context"));

    const rows = await this.db.client.select().from(policies).where(where);

    return explainPolicies(rows, context, options?.record);
  }
}
