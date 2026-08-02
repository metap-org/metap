import type { PolicyRow, RequestContext } from "./permission-service";

export type PolicyValue = { literal: unknown } | { fromContext: keyof RequestContext };

export type PolicyCondition =
  | { attribute: string; op: "eq" | "neq" | "in" | "notIn"; value: PolicyValue }
  | { all: readonly PolicyCondition[] }
  | { any: readonly PolicyCondition[] };

function resolveValue(value: PolicyValue, context: RequestContext): unknown {
  return "literal" in value ? value.literal : context[value.fromContext];
}

function matchOperator(
  op: "eq" | "neq" | "in" | "notIn",
  actual: unknown,
  expected: unknown,
): boolean {
  switch (op) {
    case "eq":
      return actual === expected;
    case "neq":
      return actual !== expected;
    case "in":
      return Array.isArray(expected) && expected.includes(actual);
    case "notIn":
      return Array.isArray(expected) && !expected.includes(actual);
  }
}

export function roleGatePassed(
  policyRoles: readonly string[] | null,
  callerRoles: readonly string[] | undefined,
): boolean {
  if (!policyRoles || policyRoles.length === 0) {
    return true;
  }
  return (callerRoles ?? []).some((role) => policyRoles.includes(role));
}

export function evaluatePolicyRow(
  policy: PolicyRow,
  context: RequestContext,
  recordSubject: Record<string, unknown> | undefined,
): boolean {
  if (!roleGatePassed(policy.roles as string[] | null, context.roles)) {
    return false;
  }

  const condition = policy.condition as PolicyCondition | null;

  if (!condition) {
    return true;
  }

  const subject = policy.subject === "record" && recordSubject ? recordSubject : context;
  return evaluateCondition(condition, subject, context) === true;
}

export function evaluateCondition(
  condition: PolicyCondition,
  subject: Record<string, unknown>,
  context: RequestContext,
): true | string {
  if ("all" in condition) {
    for (const inner of condition.all) {
      const result = evaluateCondition(inner, subject, context);
      if (result !== true) {
        return result;
      }
    }
    return true;
  }

  if ("any" in condition) {
    let lastFailure: string | undefined;
    for (const inner of condition.any) {
      const result = evaluateCondition(inner, subject, context);
      if (result === true) {
        return true;
      }
      lastFailure = result;
    }
    return lastFailure ?? "no condition in 'any' matched";
  }

  const actual = subject[condition.attribute];
  const expected = resolveValue(condition.value, context);
  const passed = matchOperator(condition.op, actual, expected);

  return passed
    ? true
    : `condition failed: ${condition.attribute} ${condition.op} ${JSON.stringify(expected)} (got ${JSON.stringify(actual)})`;
}
