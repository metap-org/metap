import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { RoleAssignmentService } from "./role-assignment-service";

const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("RoleAssignmentService (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let service: RoleAssignmentService;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000040";
  const otherTenantId = "00000000-0000-0000-0000-000000000099";
  const userId = "00000000-0000-0000-0000-000000000041";

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new RoleAssignmentService(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping RoleAssignmentService live-DB tests: could not connect to ${databaseUrl}: ${
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
    await pgClient.query("DELETE FROM user_roles WHERE tenant_id IN ($1, $2)", [
      tenantId,
      otherTenantId,
    ]);
  }

  it("returns an empty array for a user with no assigned roles", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const roles = await service.getRolesForUser(tenantId, userId);
    expect(roles).toEqual([]);
  });

  it("assigns a role and returns it from getRolesForUser", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual(["admin"]);
    } finally {
      await cleanup();
    }
  });

  it("is idempotent: assigning the same role twice does not duplicate or throw", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.assignRole(tenantId, userId, "admin", undefined);
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual(["admin"]);
    } finally {
      await cleanup();
    }
  });

  it("revokes a role; revoking a role the user does not have is a no-op", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.revokeRole(tenantId, userId, "admin");
      const roles = await service.getRolesForUser(tenantId, userId);
      expect(roles).toEqual([]);

      await expect(service.revokeRole(tenantId, userId, "admin")).resolves.toBeUndefined();
    } finally {
      await cleanup();
    }
  });

  it("listUsers groups roles by user and does not leak other tenants", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const otherUserId = "00000000-0000-0000-0000-000000000042";

    try {
      await service.assignRole(tenantId, userId, "admin", undefined);
      await service.assignRole(tenantId, userId, "editor", undefined);
      await service.assignRole(otherTenantId, otherUserId, "admin", undefined);

      const users = await service.listUsers(tenantId);
      expect(users).toHaveLength(1);
      expect(users[0]?.userId).toBe(userId);
      expect(users[0]?.roles.sort()).toEqual(["admin", "editor"]);
    } finally {
      await cleanup();
    }
  });
});
