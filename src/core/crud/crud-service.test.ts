import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { AppContainer } from "../container";
import { createContainer } from "../container";
import type { RequestContext } from "../permission/permission-service";
import type { AppConfig } from "../../server/config";

const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";
const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

describe("CrudService.update (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000020",
    userId: "00000000-0000-0000-0000-000000000021",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-update-test-"));
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
        `Skipping CrudService.update live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("updates a record when the version matches", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "U001", name: "Acme" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Acme Corp" },
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.version).toBe(created.data.version + 1);
        expect((result.data.data as { name?: string }).name).toBe("Acme Corp");
        expect((result.data.data as { code?: string }).code).toBe("U001");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("rejects an update with a stale version", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "U002", name: "Beta" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const first = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Beta One" },
        context,
      );
      expect(first.ok).toBe(true);

      const stale = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Beta Two" },
        context,
      );

      expect(stale.ok).toBe(false);
      if (!stale.ok) {
        expect(stale.status).toBe(409);
        expect(stale.error).toBe("version_conflict");
      }

      const row = await pgClient.query<{ data: { name?: string } }>(
        "SELECT data FROM records WHERE id = $1",
        [created.data.id],
      );
      expect(row.rows[0]?.data.name).toBe("Beta One");
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("ignores a client-supplied change to the workflow state field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "U003", name: "Gamma" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { status: "active" },
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect((result.data.data as { status?: string }).status).toBe("draft");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("does not allow updating a record scoped to a different tenant", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "U004", name: "Delta" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const otherTenantContext: RequestContext = {
        ...context,
        tenantId: "00000000-0000-0000-0000-000000000099",
      };

      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Delta Hijacked" },
        otherTenantContext,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(404);
        expect(result.error).toBe("record_not_found");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("rolls back the record insert when the outbox write fails", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const enqueueSpy = vi
      .spyOn(container.outbox, "enqueue")
      .mockImplementationOnce(async () => {
        throw new Error("simulated outbox failure");
      });

    try {
      await expect(
        container.crud.create(
          "crm.customers",
          { code: "U005", name: "Rollback Test" },
          context,
        ),
      ).rejects.toThrow("simulated outbox failure");

      const row = await pgClient.query("SELECT id FROM records WHERE code = $1", ["U005"]);
      expect(row.rows).toHaveLength(0);
    } finally {
      enqueueSpy.mockRestore();
      const leftover = await pgClient.query<{ id: string }>(
        "SELECT id FROM records WHERE code = $1",
        ["U005"],
      );
      for (const leftoverRow of leftover.rows) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
          leftoverRow.id,
        ]);
      }
      await pgClient.query("DELETE FROM records WHERE code = $1", ["U005"]);
    }
  });
});
