import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { AppContainer } from "../container";
import { createContainer } from "../container";
import { registerEntities } from "../../modules/registry";
import type { RequestContext } from "../permission/permission-service";
import type { AppConfig } from "../../server/config";

const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
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
    registerEntities(container.metadata);

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

describe("CrudService.transition (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

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

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-transition-test-"));
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
    registerEntities(container.metadata);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping CrudService.transition live-DB tests: could not connect to ${databaseUrl}: ${
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

  async function cleanup(recordId: string) {
    await pgClient.query("DELETE FROM workflow_events WHERE record_id = $1", [recordId]);
    await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
    await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
  }

  it("executes a valid transition: updates state/status/version, logs the event, emits the outbox event", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T001", name: "Acme", email: "acme@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.version).toBe(created.data.version + 1);
        expect(result.data.status).toBe("active");
        expect((result.data.data as { status?: string }).status).toBe("active");
      }

      const events = await pgClient.query<{ action: string; from_state: string; to_state: string }>(
        "SELECT action, from_state, to_state FROM workflow_events WHERE record_id = $1",
        [created.data.id],
      );
      expect(events.rows).toEqual([
        { action: "activate", from_state: "draft", to_state: "active" },
      ]);

      const outboxRows = await pgClient.query<{ topic: string }>(
        "SELECT topic FROM outbox_events WHERE aggregate_id = $1 AND topic = $2",
        [created.data.id, "crm.customers.workflow.transitioned"],
      );
      expect(outboxRows.rows).toHaveLength(1);
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition that is not valid from the current state", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T002", name: "Beta", email: "beta@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "block",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(409);
        expect(result.error).toBe("invalid_transition");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition blocked by a guard and surfaces the guard's reason", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T003", name: "Gamma" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(422);
        expect(result.error).toBe("guard_failed");
        expect(result.message).toBe("Email is required to activate a customer.");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition with a stale version", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T004", name: "Delta", email: "delta@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const bumped = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Delta Updated" },
        context,
      );
      expect(bumped.ok).toBe(true);

      const stale = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(stale.ok).toBe(false);
      if (!stale.ok) {
        expect(stale.status).toBe(409);
        expect(stale.error).toBe("version_conflict");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });
});

describe("CrudService field/record enforcement (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

  const tenantId = "00000000-0000-0000-0000-000000000070";
  const adminContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000071",
    roles: ["admin"],
  };
  const editorContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000072",
    roles: ["editor"],
  };
  const viewerContext: RequestContext = {
    tenantId,
    userId: "00000000-0000-0000-0000-000000000073",
    roles: ["viewer"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-enforcement-test-"));
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
    registerEntities(container.metadata);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping CrudService enforcement live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM policies WHERE tenant_id = $1", [tenantId]);
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("masks a field the caller cannot read from create/update responses", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "E001", name: "Enforcement Co", phone: "555-1000" },
        viewerContext,
      );

      expect(created.ok).toBe(true);
      if (created.ok) {
        recordId = created.data.id;
        expect((created.data.data as { phone?: string }).phone).toBeUndefined();
        expect((created.data.data as { name?: string }).name).toBe("Enforcement Co");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("rejects a create payload touching a field the caller cannot write", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "write",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    try {
      const result = await container.crud.create(
        "crm.customers",
        { code: "E002", name: "Blocked Co", phone: "555-2000" },
        viewerContext,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
        expect(result.fieldErrors).toEqual({ phone: ["forbidden"] });
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
    }
  });

  it("returns fieldErrors naming the invalid field(s) on a validation failure", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.create("crm.customers", { code: "" }, adminContext);

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.status).toBe(400);
      expect(result.fieldErrors?.code).toBeDefined();
      expect(result.fieldErrors?.name).toBeDefined();
    }
  });

  it("rejects updating a record that fails a record-level condition", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "E003", name: "Owned Co" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "update",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Hijacked" },
        editorContext,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("does not empty an admin's list() results because of a non-admin-scoped record-level read policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "E004", name: "Admin Visible Co" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["editor"],
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list("crm.customers", { limit: 20 }, adminContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.some((record) => record.id === created.data.id)).toBe(true);
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("masks the top-level code/status columns that mirror a field the caller cannot read", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const codePolicy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["admin"],
      undefined,
      undefined,
      "code",
    );
    const statusPolicy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["admin"],
      undefined,
      undefined,
      "status",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "E005", name: "Masked Mirror Co" },
        viewerContext,
      );

      expect(created.ok).toBe(true);
      if (created.ok) {
        recordId = created.data.id;
        expect((created.data.data as { code?: string }).code).toBeUndefined();
        expect((created.data.data as { status?: string }).status).toBeUndefined();
        expect(created.data.code).toBeNull();
        expect(created.data.status).toBeNull();
      }
    } finally {
      if (codePolicy) {
        await container.permissions.deletePolicy(tenantId, codePolicy.id);
      }
      if (statusPolicy) {
        await container.permissions.deletePolicy(tenantId, statusPolicy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("get() returns an existing record, field-masked the same way list() would mask it", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "G001", name: "Get Enforcement Co", phone: "555-3000" },
        adminContext,
      );
      expect(created.ok).toBe(true);
      if (!created.ok) return;
      recordId = created.data.id;

      const result = await container.crud.get("crm.customers", recordId, viewerContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect((result.data.data as { phone?: string }).phone).toBeUndefined();
        expect((result.data.data as { name?: string }).name).toBe("Get Enforcement Co");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("get() returns 404 record_not_found for a non-existent id", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.get(
      "crm.customers",
      "00000000-0000-0000-0000-000000000099",
      adminContext,
    );

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.status).toBe(404);
      expect(result.error).toBe("record_not_found");
    }
  });

  it("get() returns 403 for a non-admin blocked by a record-level read policy, but still succeeds for admin", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "G002", name: "Get Record Policy Co" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "read",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const editorResult = await container.crud.get("crm.customers", created.data.id, editorContext);
      expect(editorResult.ok).toBe(false);
      if (!editorResult.ok) {
        expect(editorResult.status).toBe(403);
      }

      const adminResult = await container.crud.get("crm.customers", created.data.id, adminContext);
      expect(adminResult.ok).toBe(true);
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("get() capabilities.writableFields excludes a field denied by write policy", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "write",
      ["admin"],
      undefined,
      undefined,
      "phone",
    );

    let recordId: string | undefined;

    try {
      const created = await container.crud.create(
        "crm.customers",
        { code: "G003", name: "Capability Co", phone: "555-4000" },
        adminContext,
      );
      expect(created.ok).toBe(true);
      if (!created.ok) return;
      recordId = created.data.id;

      const result = await container.crud.get("crm.customers", recordId, viewerContext);

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.capabilities.writableFields).not.toContain("phone");
        expect(result.data.capabilities.writableFields).toContain("name");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      if (recordId) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
      }
    }
  });

  it("get() capabilities.transitions reports the real guard result, available or not", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const createdNoEmail = await container.crud.create(
      "crm.customers",
      { code: "G004", name: "No Email Co" },
      adminContext,
    );
    expect(createdNoEmail.ok).toBe(true);
    if (!createdNoEmail.ok) return;

    const createdWithEmail = await container.crud.create(
      "crm.customers",
      { code: "G005", name: "Has Email Co", email: "g005@example.com" },
      adminContext,
    );
    expect(createdWithEmail.ok).toBe(true);
    if (!createdWithEmail.ok) return;

    try {
      const noEmailResult = await container.crud.get(
        "crm.customers",
        createdNoEmail.data.id,
        adminContext,
      );
      expect(noEmailResult.ok).toBe(true);
      if (noEmailResult.ok) {
        const activate = noEmailResult.data.capabilities.transitions.find(
          (t) => t.action === "activate",
        );
        expect(activate?.available).toBe(false);
        expect(activate?.reason).toBe("Email is required to activate a customer.");
      }

      const withEmailResult = await container.crud.get(
        "crm.customers",
        createdWithEmail.data.id,
        adminContext,
      );
      expect(withEmailResult.ok).toBe(true);
      if (withEmailResult.ok) {
        const activate = withEmailResult.data.capabilities.transitions.find(
          (t) => t.action === "activate",
        );
        expect(activate?.available).toBe(true);
        expect(activate?.reason).toBeUndefined();
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
        createdNoEmail.data.id,
      ]);
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
        createdWithEmail.data.id,
      ]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [createdNoEmail.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [createdWithEmail.data.id]);
    }
  });

  it("get() capabilities.canUpdate and transitions reflect a record-level update denial", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "G006", name: "Owned Co 2", email: "g006@example.com" },
      adminContext,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    const policy = await container.permissions.createPolicy(
      tenantId,
      "crm.customers",
      "update",
      undefined,
      { attribute: "createdBy", op: "eq", value: { fromContext: "userId" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.get("crm.customers", created.data.id, editorContext);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.capabilities.canUpdate).toBe(false);
        const activate = result.data.capabilities.transitions.find((t) => t.action === "activate");
        expect(activate?.available).toBe(false);
        expect(activate?.reason).toBe("forbidden");
      }
    } finally {
      if (policy) {
        await container.permissions.deletePolicy(tenantId, policy.id);
      }
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });
});
