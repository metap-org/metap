import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import jwt from "jsonwebtoken";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildApp } from "./app";
import type { AppConfig } from "./config";

describe("buildApp", () => {
  let app: FastifyInstance;
  let tmpDir: string;

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-auth-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl: "postgres://x:x@localhost:1/x",
      rabbitmqUrl: "amqp://x:x@localhost:1",
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    app = await buildApp(config);
  });

  afterAll(async () => {
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("allows /health without auth", async () => {
    const response = await app.inject({ method: "GET", url: "/health" });

    expect(response.statusCode).toBe(200);
  });

  it("rejects /metadata/entities without auth", async () => {
    const response = await app.inject({ method: "GET", url: "/metadata/entities" });

    expect(response.statusCode).toBe(401);
  });

  it("returns a structured 404 with requestId and traceId for unmatched routes", async () => {
    const response = await app.inject({ method: "GET", url: "/this-route-does-not-exist" });

    expect(response.statusCode).toBe(404);
    const body = response.json<{
      error: { code: string; message: string; requestId: string; traceId: string };
    }>();
    expect(body.error.code).toBe("not_found");
    expect(typeof body.error.requestId).toBe("string");
    expect(body.error.requestId.length).toBeGreaterThan(0);
    expect(typeof body.error.traceId).toBe("string");
    expect(body.error.traceId.length).toBeGreaterThan(0);
  });
});

describe("GET /metadata/entities/:entity", () => {
  let app: FastifyInstance;
  let tmpDir: string;
  let token: string;

  beforeAll(async () => {
    const { publicKey, privateKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-metadata-entity-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl: "postgres://x:x@localhost:1/x",
      rabbitmqUrl: "amqp://x:x@localhost:1",
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    app = await buildApp(config);
    token = jwt.sign(
      { tenantId: "00000000-0000-0000-0000-000000000001", roles: ["admin"] },
      privateKey,
      { algorithm: "RS256", subject: "00000000-0000-0000-0000-000000000002", expiresIn: "1h" },
    );
  });

  afterAll(async () => {
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("returns projected metadata without leaking the internal Zod schema", async () => {
    const response = await app.inject({
      method: "GET",
      url: "/metadata/entities/crm.customers",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(200);
    const body = response.json<{ data: Record<string, unknown> }>();
    expect(body.data.name).toBe("crm.customers");
    expect(body.data).not.toHaveProperty("schema");
    expect(body.data).not.toHaveProperty("tableName");
    expect(body.data).not.toHaveProperty("permissions");
  });

  it("returns a structured 404 for an unknown entity", async () => {
    const response = await app.inject({
      method: "GET",
      url: "/metadata/entities/does.not.exist",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(404);
    const body = response.json<{
      error: { code: string; message: string; requestId: string; traceId: string };
    }>();
    expect(body.error.code).toBe("entity_not_found");
    expect(body.error.message).toBe("Entity not found.");
    expect(typeof body.error.requestId).toBe("string");
    expect(typeof body.error.traceId).toBe("string");
  });
});

// This suite exercises a real HTTP POST -> CrudService.create -> Postgres insert
// round trip, so (unlike the suite above) it needs a live database and is
// skipped without one. It's a regression test for a bug where Fastify's AJV
// `removeAdditional: "all"` config silently stripped the entire `data` payload
// from `POST /api/:entity` requests before the route handler ever saw it.
describe("buildApp (record creation, live DB)", () => {
  const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
  const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

  let app: FastifyInstance;
  let tmpDir: string;
  let privateKey: string;
  let pgClient: Client;
  let dbAvailable = true;

  beforeAll(async () => {
    const keyPair = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });
    privateKey = keyPair.privateKey;

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-record-create-test-"));
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

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping live-DB record-creation test: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (pgClient && dbAvailable) {
      await pgClient.end();
    }
    await app.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("persists the posted data payload intact (regression: AJV must not strip the free-form `data` object)", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const tenantId = "00000000-0000-0000-0000-000000000010";
    const userId = "00000000-0000-0000-0000-000000000011";
    const token = jwt.sign({ tenantId, roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: userId,
      expiresIn: "1h",
    });

    let recordId: string | undefined;

    try {
      const response = await app.inject({
        method: "POST",
        url: "/api/crm.customers",
        headers: { authorization: `Bearer ${token}` },
        payload: { data: { code: "C001", name: "Acme" } },
      });

      expect(response.statusCode).toBe(201);

      const body = response.json<{
        data: { id: string; data: { code?: string; name?: string } };
      }>();

      recordId = body.data.id;
      expect(body.data.data).toMatchObject({ code: "C001", name: "Acme" });
    } finally {
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("persists the patched data payload intact (regression: AJV must not strip the free-form `data` object)", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const tenantId = "00000000-0000-0000-0000-000000000010";
    const userId = "00000000-0000-0000-0000-000000000011";
    const token = jwt.sign({ tenantId, roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: userId,
      expiresIn: "1h",
    });

    let recordId: string | undefined;

    try {
      const createResponse = await app.inject({
        method: "POST",
        url: "/api/crm.customers",
        headers: { authorization: `Bearer ${token}` },
        payload: { data: { code: "C002", name: "Acme" } },
      });

      expect(createResponse.statusCode).toBe(201);

      const createBody = createResponse.json<{
        data: { id: string; version: number; data: { code?: string; name?: string } };
      }>();

      recordId = createBody.data.id;

      const patchResponse = await app.inject({
        method: "PATCH",
        url: `/api/crm.customers/${recordId}`,
        headers: { authorization: `Bearer ${token}` },
        payload: { version: createBody.data.version, data: { name: "Acme Corp" } },
      });

      expect(patchResponse.statusCode).toBe(200);

      const patchBody = patchResponse.json<{
        data: { id: string; data: { code?: string; name?: string } };
      }>();

      expect(patchBody.data.data).toMatchObject({ code: "C002", name: "Acme Corp" });
    } finally {
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });
});
