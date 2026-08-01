import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import jwt from "jsonwebtoken";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildApp } from "../app";
import type { AppConfig } from "../config";

describe("admin routes (live DB)", () => {
  const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
  const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

  const tenantId = "00000000-0000-0000-0000-000000000050";
  const adminUserId = "00000000-0000-0000-0000-000000000051";
  // Zod v4's `.uuid()` enforces RFC 4122 version/variant nibbles (unlike v3's looser check),
  // so this fixture needs a structurally valid UUID to pass the `:userId` route param schema.
  const targetUserId = "00000000-0000-4000-8000-000000000052";

  let app: FastifyInstance;
  let tmpDir: string;
  let privateKey: string;
  let pgClient: Client;
  let dbAvailable = true;
  let adminToken: string;
  let nonAdminToken: string;

  beforeAll(async () => {
    const keyPair = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });
    privateKey = keyPair.privateKey;

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-admin-routes-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, keyPair.publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    app = await buildApp(config);

    adminToken = jwt.sign({ tenantId }, privateKey, {
      algorithm: "RS256",
      subject: adminUserId,
      expiresIn: "1h",
    });
    nonAdminToken = jwt.sign({ tenantId }, privateKey, {
      algorithm: "RS256",
      subject: targetUserId,
      expiresIn: "1h",
    });

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
      await pgClient.query(
        `INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')
         ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
        [tenantId, adminUserId],
      );
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping admin routes live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1", [tenantId]);
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("rejects a non-admin caller with 403", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/users",
      headers: { authorization: `Bearer ${nonAdminToken}` },
    });

    expect(response.statusCode).toBe(403);
    expect(response.json()).toMatchObject({ error: { code: "forbidden" } });
  });

  it("assigns a role, lists it, then revokes it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const assignResponse = await app.inject({
      method: "POST",
      url: `/admin/users/${targetUserId}/roles`,
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { role: "editor" },
    });

    expect(assignResponse.statusCode).toBe(201);
    expect(assignResponse.json()).toEqual({ data: ["editor"] });

    const listResponse = await app.inject({
      method: "GET",
      url: `/admin/users/${targetUserId}/roles`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(listResponse.statusCode).toBe(200);
    expect(listResponse.json()).toEqual({ data: ["editor"] });

    const revokeResponse = await app.inject({
      method: "DELETE",
      url: `/admin/users/${targetUserId}/roles/editor`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(revokeResponse.statusCode).toBe(200);
    expect(revokeResponse.json()).toEqual({ data: [] });
  });

  it("assigning the same role twice is idempotent", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    try {
      await app.inject({
        method: "POST",
        url: `/admin/users/${targetUserId}/roles`,
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { role: "editor" },
      });
      const second = await app.inject({
        method: "POST",
        url: `/admin/users/${targetUserId}/roles`,
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { role: "editor" },
      });

      expect(second.statusCode).toBe(201);
      expect(second.json()).toEqual({ data: ["editor"] });
    } finally {
      await pgClient.query("DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2", [
        tenantId,
        targetUserId,
      ]);
    }
  });

  it("lists users with assigned roles in the tenant", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/users",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(response.statusCode).toBe(200);
    const body = response.json<{ data: { userId: string; roles: string[] }[] }>();
    const adminEntry = body.data.find((u) => u.userId === adminUserId);
    expect(adminEntry?.roles).toEqual(["admin"]);
  });

  it("rejects a non-admin caller with 403 on /admin/policies", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "GET",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${nonAdminToken}` },
    });

    expect(response.statusCode).toBe(403);
  });

  it("creates a policy, lists it filtered by entity, then deletes it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { entity: "crm.customers", action: "update", roles: ["editor"] },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{ data: { id: string; entity: string } }>();
    expect(created.data.entity).toBe("crm.customers");

    const listResponse = await app.inject({
      method: "GET",
      url: "/admin/policies?entity=crm.customers",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(listResponse.statusCode).toBe(200);
    const listed = listResponse.json<{ data: { id: string }[] }>();
    expect(listed.data.some((p) => p.id === created.data.id)).toBe(true);

    const deleteResponse = await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });

    expect(deleteResponse.statusCode).toBe(200);

    const afterDelete = await app.inject({
      method: "GET",
      url: "/admin/policies?entity=crm.customers",
      headers: { authorization: `Bearer ${adminToken}` },
    });

    const afterDeleteBody = afterDelete.json<{ data: { id: string }[] }>();
    expect(afterDeleteBody.data.some((p) => p.id === created.data.id)).toBe(false);
  });

  it("accepts a policy with a condition", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: {
        entity: "crm.customers",
        action: "read",
        condition: { attribute: "functionId", op: "eq", value: { literal: "sales-app" } },
      },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{ data: { id: string } }>();

    await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });
  });

  it("creates a field-scoped policy with subject 'record'", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: {
        entity: "crm.customers",
        action: "write",
        field: "phone",
        subject: "record",
        condition: { attribute: "status", op: "eq", value: { literal: "draft" } },
      },
    });

    expect(createResponse.statusCode).toBe(201);
    const created = createResponse.json<{
      data: { id: string; field: string; subject: string; action: string };
    }>();
    expect(created.data.field).toBe("phone");
    expect(created.data.subject).toBe("record");
    expect(created.data.action).toBe("write");

    await app.inject({
      method: "DELETE",
      url: `/admin/policies/${created.data.id}`,
      headers: { authorization: `Bearer ${adminToken}` },
    });
  });

  it("rejects a field-scoped policy with an incoherent action", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { entity: "crm.customers", action: "create", field: "phone" },
    });

    expect(response.statusCode).toBe(400);
  });

  it("simulates a decision for a hypothetical caller via /admin/policies/explain", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createPolicyResponse = await app.inject({
      method: "POST",
      url: "/admin/policies",
      headers: { authorization: `Bearer ${adminToken}` },
      payload: { entity: "crm.customers", action: "update", roles: ["editor"] },
    });
    const policy = createPolicyResponse.json<{ data: { id: string } }>().data;

    try {
      const deniedResponse = await app.inject({
        method: "POST",
        url: "/admin/policies/explain",
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { roles: ["viewer"], entity: "crm.customers", action: "update" },
      });

      expect(deniedResponse.statusCode).toBe(200);
      const denied = deniedResponse.json<{
        data: { allowed: boolean; policiesConsidered: { roleGate: string }[] };
      }>();
      expect(denied.data.allowed).toBe(false);
      expect(denied.data.policiesConsidered[0]?.roleGate).toBe("failed");

      const passingResponse = await app.inject({
        method: "POST",
        url: "/admin/policies/explain",
        headers: { authorization: `Bearer ${adminToken}` },
        payload: { roles: ["editor"], entity: "crm.customers", action: "update" },
      });

      const passing = passingResponse.json<{ data: { allowed: boolean } }>();
      expect(passing.data.allowed).toBe(true);
    } finally {
      if (policy) {
        await app.inject({
          method: "DELETE",
          url: `/admin/policies/${policy.id}`,
          headers: { authorization: `Bearer ${adminToken}` },
        });
      }
    }
  });

  it("rejects a non-admin caller on /admin/policies/explain", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const response = await app.inject({
      method: "POST",
      url: "/admin/policies/explain",
      headers: { authorization: `Bearer ${nonAdminToken}` },
      payload: { roles: ["admin"], entity: "crm.customers", action: "update" },
    });

    expect(response.statusCode).toBe(403);
  });
});
