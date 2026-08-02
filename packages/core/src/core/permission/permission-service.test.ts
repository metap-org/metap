import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";
import { PostgresPolicyStore } from "./policy-store";
import type { PolicyStore, PolicyRow } from "./policy-store";

describe("PermissionService with an injected PolicyStore (no DB)", () => {
  function fakePolicyRow(overrides: Partial<PolicyRow>): PolicyRow {
    return {
      id: "policy-1",
      tenantId: "t1",
      entity: "some.entity",
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

  it("delegates canReadEntity through the injected PolicyStore, not a raw Database", async () => {
    const findContextPolicies = vi.fn(() => Promise.resolve<PolicyRow[]>([]));
    const store: PolicyStore = {
      findContextPolicies,
      loadAllPolicies: () => Promise.resolve([]),
      findExplainPolicies: () => Promise.resolve([]),
      listPolicies: () => Promise.resolve([]),
      createPolicy: () => Promise.resolve(fakePolicyRow({})),
      deletePolicy: () => Promise.resolve(),
    };
    const service = new PermissionService(store);

    const decision = await service.canReadEntity(
      { tenantId: "t1", roles: ["viewer"] },
      "some.entity",
    );

    expect(findContextPolicies).toHaveBeenCalledWith("t1", "some.entity", "read");
    expect(decision.allowed).toBe(true);
  });

  it("scopedTenant throws rather than silently defaulting when tenantId is empty", () => {
    const store: PolicyStore = {
      findContextPolicies: () => Promise.resolve([]),
      loadAllPolicies: () => Promise.resolve([]),
      findExplainPolicies: () => Promise.resolve([]),
      listPolicies: () => Promise.resolve([]),
      createPolicy: () => Promise.resolve(fakePolicyRow({})),
      deletePolicy: () => Promise.resolve(),
    };
    const service = new PermissionService(store);

    expect(() => service.scopedTenant({ tenantId: "" })).toThrow(/tenantId/);
  });

  it("denies when the injected PolicyStore returns a policy the caller's role doesn't match", async () => {
    const store: PolicyStore = {
      findContextPolicies: () => Promise.resolve([fakePolicyRow({ roles: ["editor"] })]),
      loadAllPolicies: () => Promise.resolve([]),
      findExplainPolicies: () => Promise.resolve([]),
      listPolicies: () => Promise.resolve([]),
      createPolicy: () => Promise.resolve(fakePolicyRow({})),
      deletePolicy: () => Promise.resolve(),
    };
    const service = new PermissionService(store);

    const decision = await service.canReadEntity(
      { tenantId: "t1", roles: ["viewer"] },
      "some.entity",
    );

    expect(decision.allowed).toBe(false);
    expect(decision.reason).toBe("forbidden");
  });
});

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("PermissionService (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let service: PermissionService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000060";
  const entity = "test.restricted";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new PermissionService(new PostgresPolicyStore(db));

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping PermissionService live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.end();
    }
    await db.close();
  });

  async function cleanup() {
    await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
  }

  function contextWithRoles(roles: string[], extra?: Partial<RequestContext>): RequestContext {
    return { tenantId, roles, ...extra };
  }

  it("allows admin regardless of any policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "create", ["editor"], undefined, undefined);
      const decision = await service.canCreateEntity(contextWithRoles(["admin"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("allows any role when the entity has no policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const decision = await service.canReadEntity(
      contextWithRoles(["nobody-in-particular"]),
      entity,
    );
    expect(decision.allowed).toBe(true);
  });

  it("allows a role listed on a matching policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        ["viewer", "editor"],
        undefined,
        undefined,
      );
      const decision = await service.canReadEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("denies a role not listed on any matching policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "create", ["editor"], undefined, undefined);
      const decision = await service.canCreateEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(false);
      expect(decision.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("evaluates a condition gate in addition to the role gate", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        ["editor"],
        { attribute: "functionId", op: "eq", value: { literal: "sales-app" } },
        undefined,
      );

      const passing = await service.canUpdateEntity(
        contextWithRoles(["editor"], { functionId: "sales-app" }),
        entity,
      );
      expect(passing.allowed).toBe(true);

      const failing = await service.canUpdateEntity(
        contextWithRoles(["editor"], { functionId: "other-app" }),
        entity,
      );
      expect(failing.allowed).toBe(false);
      expect(failing.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("ORs multiple policies for the same action: one passing is enough", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        ["impossible-role"],
        undefined,
        undefined,
      );
      await service.createPolicy(tenantId, entity, "update", ["editor"], undefined, undefined);

      const decision = await service.canUpdateEntity(contextWithRoles(["editor"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("does not apply another tenant's policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const otherTenantId = "00000000-0000-0000-0000-000000000061";

    try {
      await service.createPolicy(otherTenantId, entity, "create", ["editor"], undefined, undefined);

      const decision = await service.canCreateEntity(
        contextWithRoles(["nobody-in-particular"]),
        entity,
      );
      expect(decision.allowed).toBe(true);
    } finally {
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [otherTenantId]);
    }
  });

  it("checkAction ignores field-scoped and record-scoped policies when checking entity-level actions", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      // A field-scoped "read" policy that would deny 'viewer' if (incorrectly)
      // picked up by the entity-level check.
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        ["someone-else"],
        undefined,
        undefined,
        "salary",
      );
      // A record-scoped "read" policy with a condition that can never pass
      // against a bare context (no record data) — would incorrectly deny
      // entity-level read if (incorrectly) picked up by checkAction.
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        undefined,
        { attribute: "status", op: "eq", value: { literal: "active" } },
        undefined,
        undefined,
        "record",
      );

      const decision = await service.canReadEntity(contextWithRoles(["viewer"]), entity);
      expect(decision.allowed).toBe(true);
    } finally {
      await cleanup();
    }
  });

  it("explain reports an entity-level policy's role and condition gates", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        ["editor"],
        { attribute: "functionId", op: "eq", value: { literal: "sales-app" } },
        undefined,
      );

      const denied = await service.explain(contextWithRoles(["viewer"]), entity, "update");
      expect(denied.allowed).toBe(false);
      expect(denied.policiesConsidered).toHaveLength(1);
      expect(denied.policiesConsidered[0]?.roleGate).toBe("failed");

      const passing = await service.explain(
        contextWithRoles(["editor"], { functionId: "sales-app" }),
        entity,
        "update",
      );
      expect(passing.allowed).toBe(true);
      expect(passing.policiesConsidered[0]?.roleGate).toBe("passed");
      expect(passing.policiesConsidered[0]?.conditionGate).toBe("passed");
    } finally {
      await cleanup();
    }
  });

  it("explain reports a field-scoped policy when a field is given", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "read", ["hr"], undefined, undefined, "salary");

      const result = await service.explain(contextWithRoles(["viewer"]), entity, "read", {
        field: "salary",
      });
      expect(result.allowed).toBe(false);
      expect(result.policiesConsidered).toHaveLength(1);
    } finally {
      await cleanup();
    }
  });

  it("explain reports a record-scoped policy when a record is given", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        undefined,
        { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
        undefined,
        undefined,
        "record",
      );

      const owned = await service.explain(
        contextWithRoles(["editor"], { userId: "user-1" }),
        entity,
        "update",
        { record: { createdBy: "user-1" } },
      );
      expect(owned.allowed).toBe(true);

      const notOwned = await service.explain(
        contextWithRoles(["editor"], { userId: "user-1" }),
        entity,
        "update",
        { record: { createdBy: "someone-else" } },
      );
      expect(notOwned.allowed).toBe(false);
    } finally {
      await cleanup();
    }
  });
});
