import { evaluateCondition, roleGatePassed } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";
import type { PolicyRow, RequestContext } from "./permission-service";

export type PolicyTraceEntry = {
  policyId: string;
  roleGate: "open" | "passed" | "failed";
  conditionGate: "open" | "passed" | "failed";
  conditionReason?: string;
};

export type PolicyExplanation = {
  allowed: boolean;
  policiesConsidered: PolicyTraceEntry[];
};

export function explainPolicies(
  policyRows: PolicyRow[],
  context: RequestContext,
  subject: Record<string, unknown> | undefined,
): PolicyExplanation {
  if (policyRows.length === 0) {
    return { allowed: true, policiesConsidered: [] };
  }

  const entries: PolicyTraceEntry[] = policyRows.map((row) => {
    const policyRoles = row.roles as string[] | null;
    const rolePassed = roleGatePassed(policyRoles, context.roles);
    const roleGate: PolicyTraceEntry["roleGate"] =
      !policyRoles || policyRoles.length === 0 ? "open" : rolePassed ? "passed" : "failed";

    if (!rolePassed) {
      return { policyId: row.id, roleGate, conditionGate: "open" };
    }

    const condition = row.condition as PolicyCondition | null;

    if (!condition) {
      return { policyId: row.id, roleGate, conditionGate: "open" };
    }

    const conditionSubject = row.subject === "record" && subject ? subject : context;
    const result = evaluateCondition(condition, conditionSubject, context);

    if (result === true) {
      return { policyId: row.id, roleGate, conditionGate: "passed" };
    }

    return { policyId: row.id, roleGate, conditionGate: "failed", conditionReason: result };
  });

  const allowed = entries.some(
    (entry) => entry.roleGate !== "failed" && entry.conditionGate !== "failed",
  );

  return { allowed, policiesConsidered: entries };
}
