import { describe, expect, it } from "vitest";
import type { PolicyRow, RequestContext } from "./permission-service";
import { explainPolicies } from "./policy-explainer";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
  roles: ["viewer"],
};

function policyRow(overrides: Partial<PolicyRow>): PolicyRow {
  return {
    id: "policy-1",
    tenantId: context.tenantId,
    entity: "test.widgets",
    action: "read",
    field: null,
    subject: "context",
    roles: null,
    condition: null,
    createdAt: new Date(),
    createdBy: null,
    ...overrides,
  };
}

describe("explainPolicies", () => {
  it("allows with an empty trace when there are no policies", () => {
    const result = explainPolicies([], context, undefined);
    expect(result).toEqual({ allowed: true, policiesConsidered: [] });
  });

  it("marks the role gate 'open' when the policy has no role restriction", () => {
    const result = explainPolicies([policyRow({ id: "p1" })], context, undefined);
    expect(result.allowed).toBe(true);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "open", conditionGate: "open" },
    ]);
  });

  it("marks the role gate 'failed' and short-circuits the condition gate", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          roles: ["editor"],
          condition: { attribute: "status", op: "eq", value: { literal: "active" } },
        }),
      ],
      context,
      { status: "active" },
    );
    expect(result.allowed).toBe(false);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "failed", conditionGate: "open" },
    ]);
  });

  it("marks the condition gate 'failed' with a reason when the role gate passes", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          roles: ["viewer"],
          condition: { attribute: "status", op: "eq", value: { literal: "active" } },
        }),
      ],
      context,
      { status: "draft" },
    );
    expect(result.allowed).toBe(false);
    expect(result.policiesConsidered).toHaveLength(1);
    expect(result.policiesConsidered[0]).toMatchObject({
      policyId: "p1",
      roleGate: "passed",
      conditionGate: "failed",
    });
    expect(typeof result.policiesConsidered[0]?.conditionReason).toBe("string");
  });

  it("is allowed overall if any one policy fully passes, even if others fail", () => {
    const result = explainPolicies(
      [policyRow({ id: "p1", roles: ["editor"] }), policyRow({ id: "p2", roles: ["viewer"] })],
      context,
      undefined,
    );
    expect(result.allowed).toBe(true);
    expect(result.policiesConsidered).toEqual([
      { policyId: "p1", roleGate: "failed", conditionGate: "open" },
      { policyId: "p2", roleGate: "passed", conditionGate: "open" },
    ]);
  });

  it("uses the record subject only for policies with subject 'record'", () => {
    const result = explainPolicies(
      [
        policyRow({
          id: "p1",
          subject: "record",
          condition: { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
        }),
      ],
      context,
      { createdBy: "00000000-0000-0000-0000-000000000002" },
    );
    expect(result.allowed).toBe(true);
  });
});
