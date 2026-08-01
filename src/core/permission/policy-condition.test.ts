import { describe, expect, it } from "vitest";
import type { RequestContext } from "./permission-service";
import { evaluateCondition, roleGatePassed } from "./policy-condition";
import type { PolicyCondition } from "./policy-condition";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
  functionId: "sales-app",
};

describe("evaluateCondition", () => {
  it("passes an eq condition against a literal value", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "eq",
      value: { literal: "active" },
    };
    expect(evaluateCondition(condition, { status: "active" }, context)).toBe(true);
  });

  it("fails an eq condition and returns a string reason", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "eq",
      value: { literal: "active" },
    };
    const result = evaluateCondition(condition, { status: "draft" }, context);
    expect(result).not.toBe(true);
    expect(typeof result).toBe("string");
  });

  it("evaluates neq", () => {
    const condition: PolicyCondition = {
      attribute: "status",
      op: "neq",
      value: { literal: "blocked" },
    };
    expect(evaluateCondition(condition, { status: "active" }, context)).toBe(true);
    expect(evaluateCondition(condition, { status: "blocked" }, context)).not.toBe(true);
  });

  it("evaluates in and notIn against a literal array", () => {
    const inCondition: PolicyCondition = {
      attribute: "status",
      op: "in",
      value: { literal: ["draft", "active"] },
    };
    expect(evaluateCondition(inCondition, { status: "active" }, context)).toBe(true);
    expect(evaluateCondition(inCondition, { status: "blocked" }, context)).not.toBe(true);

    const notInCondition: PolicyCondition = {
      attribute: "status",
      op: "notIn",
      value: { literal: ["blocked"] },
    };
    expect(evaluateCondition(notInCondition, { status: "active" }, context)).toBe(true);
  });

  it("resolves value from context via fromContext", () => {
    const condition: PolicyCondition = {
      attribute: "createdBy",
      op: "eq",
      value: { fromContext: "userId" },
    };
    expect(
      evaluateCondition(condition, { createdBy: "00000000-0000-0000-0000-000000000002" }, context),
    ).toBe(true);
    expect(
      evaluateCondition(condition, { createdBy: "someone-else" }, context),
    ).not.toBe(true);
  });

  it("requires every condition in 'all' to pass", () => {
    const condition: PolicyCondition = {
      all: [
        { attribute: "status", op: "eq", value: { literal: "active" } },
        { attribute: "region", op: "eq", value: { literal: "vn" } },
      ],
    };
    expect(evaluateCondition(condition, { status: "active", region: "vn" }, context)).toBe(true);
    expect(
      evaluateCondition(condition, { status: "active", region: "us" }, context),
    ).not.toBe(true);
  });

  it("requires at least one condition in 'any' to pass", () => {
    const condition: PolicyCondition = {
      any: [
        { attribute: "status", op: "eq", value: { literal: "active" } },
        { attribute: "status", op: "eq", value: { literal: "draft" } },
      ],
    };
    expect(evaluateCondition(condition, { status: "draft" }, context)).toBe(true);
    expect(evaluateCondition(condition, { status: "blocked" }, context)).not.toBe(true);
  });

  it("evaluates a context-only condition using context as its own subject", () => {
    const condition: PolicyCondition = {
      attribute: "functionId",
      op: "eq",
      value: { literal: "sales-app" },
    };
    expect(
      evaluateCondition(condition, context as unknown as Record<string, unknown>, context),
    ).toBe(true);
  });
});

describe("roleGatePassed", () => {
  it("passes when the policy has no role restriction", () => {
    expect(roleGatePassed(null, ["viewer"])).toBe(true);
    expect(roleGatePassed([], ["viewer"])).toBe(true);
  });

  it("passes when the caller has one of the listed roles", () => {
    expect(roleGatePassed(["editor", "viewer"], ["viewer"])).toBe(true);
  });

  it("fails when the caller has none of the listed roles", () => {
    expect(roleGatePassed(["editor"], ["viewer"])).toBe(false);
    expect(roleGatePassed(["editor"], undefined)).toBe(false);
  });
});
