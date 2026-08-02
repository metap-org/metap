import { and, eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { policies } from "../../infra/db/schema";
import { evaluatePolicyRow } from "./policy-condition";
import type { EntityAction, PermissionDecision, PolicyRow, RequestContext } from "./permission-service";

export class PermissionSnapshot {
  private constructor(
    private readonly fieldPolicies: PolicyRow[],
    private readonly recordPoliciesByAction: Map<string, PolicyRow[]>,
  ) {}

  static async load(db: Database, tenantId: string, entity: string): Promise<PermissionSnapshot> {
    const rows = await db.client
      .select()
      .from(policies)
      .where(and(eq(policies.tenantId, tenantId), eq(policies.entity, entity)));

    const fieldPolicies = rows.filter((row) => row.field !== null);

    const recordPoliciesByAction = new Map<string, PolicyRow[]>();
    for (const row of rows) {
      if (row.field === null && row.subject === "record") {
        const list = recordPoliciesByAction.get(row.action) ?? [];
        list.push(row);
        recordPoliciesByAction.set(row.action, list);
      }
    }

    return new PermissionSnapshot(fieldPolicies, recordPoliciesByAction);
  }

  getRecordPolicies(action: EntityAction): PolicyRow[] {
    return this.recordPoliciesByAction.get(action) ?? [];
  }

  filterReadableFields(
    context: RequestContext,
    record: Record<string, unknown>,
  ): Record<string, unknown> {
    if (context.roles?.includes("admin")) {
      return record;
    }

    const readPoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of this.fieldPolicies) {
      if (policy.action !== "read" || !policy.field) {
        continue;
      }
      const list = readPoliciesByField.get(policy.field) ?? [];
      list.push(policy);
      readPoliciesByField.set(policy.field, list);
    }

    const result: Record<string, unknown> = {};

    for (const [key, value] of Object.entries(record)) {
      const fieldReadPolicies = readPoliciesByField.get(key);

      if (!fieldReadPolicies || fieldReadPolicies.length === 0) {
        result[key] = value;
        continue;
      }

      const passed = fieldReadPolicies.some((policy) => evaluatePolicyRow(policy, context, record));

      if (passed) {
        result[key] = value;
      }
    }

    return result;
  }

  writableFields(
    context: RequestContext,
    allFieldNames: readonly string[],
    existingRecord: Record<string, unknown> | undefined,
  ): string[] {
    if (context.roles?.includes("admin")) {
      return [...allFieldNames];
    }

    const writePoliciesByField = new Map<string, PolicyRow[]>();
    for (const policy of this.fieldPolicies) {
      if (policy.action !== "write" || !policy.field) {
        continue;
      }
      const list = writePoliciesByField.get(policy.field) ?? [];
      list.push(policy);
      writePoliciesByField.set(policy.field, list);
    }

    return allFieldNames.filter((field) => {
      const policies = writePoliciesByField.get(field);
      if (!policies || policies.length === 0) {
        return true;
      }
      return policies.some((policy) => evaluatePolicyRow(policy, context, existingRecord));
    });
  }

  assertWritableFields(
    context: RequestContext,
    payloadFields: readonly string[],
    existingRecord: Record<string, unknown> | undefined,
  ): PermissionDecision {
    if (context.roles?.includes("admin")) {
      return { allowed: true };
    }

    const writable = new Set(this.writableFields(context, payloadFields, existingRecord));
    const deniedField = payloadFields.find((field) => !writable.has(field));

    return deniedField ? { allowed: false, reason: "forbidden", field: deniedField } : { allowed: true };
  }

  canUpdateRecordCondition(
    context: RequestContext,
    record: Record<string, unknown>,
    action: EntityAction = "update",
  ): PermissionDecision {
    const recordPolicies = this.getRecordPolicies(action);

    if (context.roles?.includes("admin") || recordPolicies.length === 0) {
      return { allowed: true };
    }

    const passed = recordPolicies.some((policy) => evaluatePolicyRow(policy, context, record));

    return passed ? { allowed: true } : { allowed: false, reason: "forbidden" };
  }
}
