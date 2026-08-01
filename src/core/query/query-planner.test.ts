import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { AppContainer } from "../container";
import { createContainer } from "../container";
import type { RequestContext } from "../permission/permission-service";
import type { AppConfig } from "../../server/config";

const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

describe("QueryPlanner (via CrudService.list, live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000030",
    userId: "00000000-0000-0000-0000-000000000031",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    container = createContainer(config);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping QueryPlanner live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { code: "Q001", name: "Acme Corp", status: "active" },
      { code: "Q002", name: "Acme Industries", status: "draft" },
      { code: "Q003", name: "Beta LLC", status: "active" },
    ];

    for (const seed of seedRecords) {
      const created = await container.crud.create("crm.customers", seed, context);
      if (created.ok) {
        createdIds.push(created.data.id);
      }
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      for (const id of createdIds) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [id]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [id]);
      }
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("filters by an allowed equality field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { status: "active" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const names = result.data.map((record) => (record.data as { name?: string }).name);
      expect(names.sort()).toEqual(["Acme Corp", "Beta LLC"].sort());
    }
  });

  it("filters by an allowed searchable field using contains", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { name: "Acme" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const names = result.data.map((record) => (record.data as { name?: string }).name);
      expect(names.sort()).toEqual(["Acme Corp", "Acme Industries"].sort());
    }
  });

  it("silently ignores an unrecognized filter key", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { notARealField: "whatever" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data.length).toBe(3);
    }
  });

  it("sorts by an explicit allowed field in both directions", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const ascending = await container.crud.list(
      "crm.customers",
      { limit: 30, sort: "name" },
      context,
    );
    const descending = await container.crud.list(
      "crm.customers",
      { limit: 30, sort: "-name" },
      context,
    );

    expect(ascending.ok).toBe(true);
    expect(descending.ok).toBe(true);
    if (ascending.ok && descending.ok) {
      const ascNames = ascending.data.map((record) => (record.data as { name?: string }).name);
      const descNames = descending.data.map((record) => (record.data as { name?: string }).name);
      expect(ascNames).toEqual([...descNames].reverse());
      expect(ascNames[0]).toBe("Acme Corp");
    }
  });

  it("falls back to the default sort when given an invalid sort field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, sort: "notASortableField" },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const ids = result.data.map((record) => record.id);
      expect(ids).toEqual([...createdIds].reverse());
    }
  });

  it("treats a hostile filter value as data, not SQL", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { status: "active' OR '1'='1" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data.length).toBe(0);
    }
  });

  it("filters list() results by a record-level read policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const nonAdminContext: RequestContext = {
      tenantId: context.tenantId,
      userId: "00000000-0000-0000-0000-000000000032",
      roles: ["viewer"],
    };

    const policy = await container.permissions.createPolicy(
      context.tenantId,
      "crm.customers",
      "read",
      undefined,
      { attribute: "status", op: "eq", value: { literal: "active" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list(
        "crm.customers",
        { limit: 30 },
        nonAdminContext,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        const statuses = result.data.map((record) => (record.data as { status?: string }).status);
        expect(statuses.every((status) => status === "active")).toBe(true);
        expect(statuses.length).toBe(2);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(context.tenantId, policy.id);
      }
    }
  });

  it("denies all rows when a record-level read policy's role gate matches no one", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const nonAdminContext: RequestContext = {
      tenantId: context.tenantId,
      userId: "00000000-0000-0000-0000-000000000033",
      roles: ["viewer"],
    };

    const policy = await container.permissions.createPolicy(
      context.tenantId,
      "crm.customers",
      "read",
      ["nobody-has-this-role"],
      undefined,
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list("crm.customers", { limit: 30 }, nonAdminContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.length).toBe(0);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(context.tenantId, policy.id);
      }
    }
  });
});
