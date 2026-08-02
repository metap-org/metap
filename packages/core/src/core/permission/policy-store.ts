import { and, eq, isNull } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import type { PolicyCondition } from "./policy-condition";

export type PolicyRow = typeof policies.$inferSelect;

export interface PolicyStore {
  findContextPolicies(tenantId: string, entity: string, action: string): Promise<PolicyRow[]>;
  loadAllPolicies(tenantId: string, entity: string): Promise<PolicyRow[]>;
  findExplainPolicies(
    tenantId: string,
    entity: string,
    action: string,
    options?: { field?: string; subject?: "context" | "record" },
  ): Promise<PolicyRow[]>;
  listPolicies(tenantId: string, entity?: string): Promise<PolicyRow[]>;
  createPolicy(
    tenantId: string,
    entity: string,
    action: string,
    roles: string[] | undefined,
    condition: PolicyCondition | undefined,
    createdBy: string | undefined,
    field?: string,
    subject?: "context" | "record",
  ): Promise<PolicyRow>;
  deletePolicy(tenantId: string, id: string): Promise<void>;
}

export class PostgresPolicyStore implements PolicyStore {
  constructor(private readonly db: Database) {}

  async findContextPolicies(
    tenantId: string,
    entity: string,
    action: string,
  ): Promise<PolicyRow[]> {
    return this.db.client
      .select()
      .from(policies)
      .where(
        and(
          eq(policies.tenantId, tenantId),
          eq(policies.entity, entity),
          eq(policies.action, action),
          isNull(policies.field),
          eq(policies.subject, "context"),
        ),
      );
  }

  async loadAllPolicies(tenantId: string, entity: string): Promise<PolicyRow[]> {
    return this.db.client
      .select()
      .from(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.entity, entity)));
  }

  async findExplainPolicies(
    tenantId: string,
    entity: string,
    action: string,
    options?: { field?: string; subject?: "context" | "record" },
  ): Promise<PolicyRow[]> {
    const base = [
      eq(policies.tenantId, tenantId),
      eq(policies.entity, entity),
      eq(policies.action, action),
    ];

    const where = options?.field
      ? and(...base, eq(policies.field, options.field))
      : and(...base, isNull(policies.field), eq(policies.subject, options?.subject ?? "context"));

    return this.db.client.select().from(policies).where(where);
  }

  async listPolicies(tenantId: string, entity?: string): Promise<PolicyRow[]> {
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
  ): Promise<PolicyRow> {
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

    const row = inserted[0];
    if (!row) {
      throw new Error("Failed to insert policy");
    }
    return row;
  }

  async deletePolicy(tenantId: string, id: string): Promise<void> {
    await this.db.client
      .delete(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.id, id)));
  }
}
