import { describe, expect, it } from "vitest";
import { testWidgetEntity } from "../__fixtures__/test-widget.entity";
import type { OutboxService } from "../outbox/outbox-service";
import type { RequestContext } from "../permission/permission-service";
import { WorkflowEngine } from "./workflow-engine";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
};

describe("WorkflowEngine.findTransition", () => {
  const engine = new WorkflowEngine({} as unknown as OutboxService);

  it("finds a transition matching the action and current state", () => {
    const transition = engine.findTransition(testWidgetEntity, "activate", "draft");
    expect(transition?.to).toBe("active");
  });

  it("returns undefined when the action does not exist", () => {
    expect(engine.findTransition(testWidgetEntity, "nope", "draft")).toBeUndefined();
  });

  it("returns undefined when the action does not apply to the current state", () => {
    expect(engine.findTransition(testWidgetEntity, "block", "draft")).toBeUndefined();
  });
});

describe("WorkflowEngine.runGuard", () => {
  const engine = new WorkflowEngine({} as unknown as OutboxService);

  it("allows a transition with no guard", () => {
    const transition = engine.findTransition(testWidgetEntity, "block", "active");
    if (!transition) throw new Error("expected transition");
    expect(engine.runGuard(transition, {}, context)).toBe(true);
  });

  it("allows a guarded transition when the guard passes", () => {
    const transition = engine.findTransition(testWidgetEntity, "activate", "draft");
    if (!transition) throw new Error("expected transition");
    expect(engine.runGuard(transition, { email: "a@b.com" }, context)).toBe(true);
  });

  it("blocks a guarded transition and returns the guard's reason", () => {
    const transition = engine.findTransition(testWidgetEntity, "activate", "draft");
    if (!transition) throw new Error("expected transition");
    const result = engine.runGuard(transition, {}, context);
    expect(result).toBe("Email is required to activate a customer.");
  });
});
