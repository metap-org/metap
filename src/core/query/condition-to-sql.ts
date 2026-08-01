import type { SQL } from "drizzle-orm";
import { and, or, sql } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { PolicyCondition, PolicyValue } from "../permission/policy-condition";
import { roleGatePassed } from "../permission/policy-condition";
import type { PolicyRow, RequestContext } from "../permission/permission-service";

function fieldExpression(fieldName: string) {
  if (fieldName === "createdBy") {
    return records.createdBy;
  }
  if (fieldName === "updatedBy") {
    return records.updatedBy;
  }
  if (fieldName === "status") {
    return records.status;
  }
  if (fieldName === "createdAt") {
    return records.createdAt;
  }
  if (fieldName === "updatedAt") {
    return records.updatedAt;
  }
  return sql`jsonb_extract_path_text(${records.data}, ${fieldName})`;
}

function resolveValue(value: PolicyValue, context: RequestContext): unknown {
  return "literal" in value ? value.literal : context[value.fromContext];
}

export function conditionToSql(condition: PolicyCondition, context: RequestContext): SQL {
  if ("all" in condition) {
    const clauses = condition.all.map((inner) => conditionToSql(inner, context));
    return clauses.length > 0 ? and(...clauses)! : sql`true`;
  }

  if ("any" in condition) {
    const clauses = condition.any.map((inner) => conditionToSql(inner, context));
    return clauses.length > 0 ? or(...clauses)! : sql`false`;
  }

  const expr = fieldExpression(condition.attribute);
  const expected = resolveValue(condition.value, context);

  switch (condition.op) {
    case "eq":
      return sql`${expr} = ${expected}`;
    case "neq":
      return sql`${expr} != ${expected}`;
    case "in": {
      const values = Array.isArray(expected) ? expected : [];
      return values.length > 0
        ? sql`${expr} IN (${sql.join(
            values.map((value) => sql`${value}`),
            sql`, `,
          )})`
        : sql`false`;
    }
    case "notIn": {
      const values = Array.isArray(expected) ? expected : [];
      return values.length > 0
        ? sql`${expr} NOT IN (${sql.join(
            values.map((value) => sql`${value}`),
            sql`, `,
          )})`
        : sql`true`;
    }
  }
}

export function recordPolicyWhereClause(
  rows: PolicyRow[],
  context: RequestContext,
): SQL | undefined {
  if (rows.length === 0) {
    return undefined;
  }

  const passingRows = rows.filter((row) =>
    roleGatePassed(row.roles as string[] | null, context.roles),
  );

  if (passingRows.length === 0) {
    return sql`false`;
  }

  const clauses = passingRows.map((row) => {
    const condition = row.condition as PolicyCondition | null;
    return condition ? conditionToSql(condition, context) : sql`true`;
  });

  return or(...clauses);
}
