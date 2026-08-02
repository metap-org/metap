import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { PermissionSnapshot } from "./permission-snapshot";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";
import { PostgresPolicyStore } from "./policy-store";
import type { PolicyStore } from "./policy-store";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("PermissionSnapshot (live DB)", () => {
  let db: Database;
  let store: PolicyStore;
  let pgClient: Client;
  let service: PermissionService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000080";
  const entity = "test.snapshot";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    store = new PostgresPolicyStore(db);
    service = new PermissionService(store);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping PermissionSnapshot live-DB tests: could not connect to ${databaseUrl}: ${
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

  it("filterReadableFields strips a field the caller cannot read", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "read", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);
      const record = { name: "Alice", salary: 100000 };

      const asHr = snapshot.filterReadableFields(contextWithRoles(["hr"]), record);
      expect(asHr).toEqual({ name: "Alice", salary: 100000 });

      const asViewer = snapshot.filterReadableFields(contextWithRoles(["viewer"]), record);
      expect(asViewer).toEqual({ name: "Alice" });
    } finally {
      await cleanup();
    }
  });

  it("filterReadableFields evaluates a record-subject condition per field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        undefined,
        { attribute: "status", op: "eq", value: { literal: "active" } },
        undefined,
        "internalNotes",
        "record",
      );

      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);
      const activeRecord = { status: "active", internalNotes: "secret" };
      const draftRecord = { status: "draft", internalNotes: "secret" };

      expect(snapshot.filterReadableFields(contextWithRoles(["viewer"]), activeRecord)).toEqual(
        activeRecord,
      );
      expect(snapshot.filterReadableFields(contextWithRoles(["viewer"]), draftRecord)).toEqual({
        status: "draft",
      });
    } finally {
      await cleanup();
    }
  });

  it("assertWritableFields rejects a payload touching a field the caller cannot write", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "write", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);

      const allowed = snapshot.assertWritableFields(
        contextWithRoles(["hr"]),
        ["name", "salary"],
        undefined,
      );
      expect(allowed.allowed).toBe(true);

      const denied = snapshot.assertWritableFields(
        contextWithRoles(["viewer"]),
        ["name", "salary"],
        undefined,
      );
      expect(denied.allowed).toBe(false);
      expect(denied.reason).toBe("forbidden");
    } finally {
      await cleanup();
    }
  });

  it("writableFields returns only the fields allowed for a non-admin caller", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(tenantId, entity, "write", ["hr"], undefined, undefined, "salary");
      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);

      const asHr = snapshot.writableFields(contextWithRoles(["hr"]), ["name", "salary"], undefined);
      expect(asHr).toEqual(["name", "salary"]);

      const asViewer = snapshot.writableFields(
        contextWithRoles(["viewer"]),
        ["name", "salary"],
        undefined,
      );
      expect(asViewer).toEqual(["name"]);
    } finally {
      await cleanup();
    }
  });

  it("canUpdateRecordCondition evaluates against the record, not context, defaulting to the 'update' action", async (ctx) => {
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

      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);
      const callerContext = contextWithRoles(["editor"], { userId: "user-1" });

      const owned = snapshot.canUpdateRecordCondition(callerContext, { createdBy: "user-1" });
      expect(owned.allowed).toBe(true);

      const notOwned = snapshot.canUpdateRecordCondition(callerContext, {
        createdBy: "someone-else",
      });
      expect(notOwned.allowed).toBe(false);
    } finally {
      await cleanup();
    }
  });

  it("getRecordPolicies returns only rows matching the requested action", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.createPolicy(
        tenantId,
        entity,
        "read",
        undefined,
        undefined,
        undefined,
        undefined,
        "record",
      );
      await service.createPolicy(
        tenantId,
        entity,
        "update",
        undefined,
        undefined,
        undefined,
        undefined,
        "record",
      );

      const snapshot = await PermissionSnapshot.load(store, tenantId, entity);
      expect(snapshot.getRecordPolicies("read")).toHaveLength(1);
      expect(snapshot.getRecordPolicies("update")).toHaveLength(1);
      expect(snapshot.getRecordPolicies("create")).toHaveLength(0);
    } finally {
      await cleanup();
    }
  });
});
