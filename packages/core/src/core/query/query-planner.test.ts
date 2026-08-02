import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { z } from "zod";
import type { AppContainer } from "../container";
import { createContainer } from "../container";
import { testWidgetEntity } from "../__fixtures__/test-widget.entity";
import type { EntityDefinition } from "../metadata/entity";
import type { RequestContext } from "../permission/permission-service";
import type { AppConfig } from "../../server/config";
import { encodeCursor } from "./cursor";
import { InvalidCursorError } from "./query-planner";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
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
    container.metadata.register(testWidgetEntity);

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
      const created = await container.crud.create("test.widgets", seed, context);
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
      "test.widgets",
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
      "test.widgets",
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
      "test.widgets",
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
      "test.widgets",
      { limit: 30, sort: "name" },
      context,
    );
    const descending = await container.crud.list(
      "test.widgets",
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
      "test.widgets",
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
      "test.widgets",
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
      "test.widgets",
      "read",
      undefined,
      { attribute: "status", op: "eq", value: { literal: "active" } },
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list("test.widgets", { limit: 30 }, nonAdminContext);

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
      "test.widgets",
      "read",
      ["nobody-has-this-role"],
      undefined,
      undefined,
      undefined,
      "record",
    );

    try {
      const result = await container.crud.list("test.widgets", { limit: 30 }, nonAdminContext);

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

describe("QueryPlanner searchMode: 'fts' (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000040",
    userId: "00000000-0000-0000-0000-000000000041",
    roles: ["admin"],
  };

  const ftsTestEntity: EntityDefinition = {
    name: "test.fts_entries",
    label: "FTS Test Entry",
    tableName: "records",
    schema: z.object({ title: z.string(), code: z.string() }),
    fields: [
      { name: "title", label: "Title", kind: "string", searchable: true, searchMode: "fts" },
      { name: "code", label: "Code", kind: "string", searchable: true },
    ],
    listViews: [
      {
        name: "default",
        label: "Default",
        fields: ["title", "code"],
        filters: ["title", "code"],
        maxLimit: 50,
      },
    ],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-fts-test-"));
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
    container.metadata.register(testWidgetEntity);
    container.metadata.register(ftsTestEntity);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping QueryPlanner FTS live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { title: "Quick Brown Fox", code: "T001" },
      { title: "Lazy Dog Sleeps", code: "T002" },
      { title: "Brown Bear Wakes", code: "T003" },
    ];

    for (const seed of seedRecords) {
      const created = await container.crud.create("test.fts_entries", seed, context);
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

  it("matches a searchMode: 'fts' field regardless of word order", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { title: "brown quick" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const titles = result.data.map((record) => (record.data as { title?: string }).title);
      expect(titles).toEqual(["Quick Brown Fox"]);
    }
  });

  it("does not match a searchMode: 'fts' field on an unrelated word", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { title: "elephant" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data).toHaveLength(0);
    }
  });

  it("still substring-matches a plain searchable field with no searchMode — no regression", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { code: "T00" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data).toHaveLength(3);
    }
  });
});

describe("QueryPlanner cursor validation", () => {
  let container: AppContainer;
  let tmpDir: string;

  beforeAll(() => {
    // No live-DB gating needed here — planList only builds a query object,
    // it never executes anything, so the only setup this needs is a real
    // (never-connected-to) container. createContainer still eagerly reads
    // the JWT public key file at construction time (createJwtVerifier does
    // fs.readFileSync synchronously), so a real keypair is required even
    // though auth is never exercised in these tests.
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-cursor-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    container = createContainer({
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    });
    container.metadata.register(testWidgetEntity);
  });

  afterAll(async () => {
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("throws InvalidCursorError when the cursor's field doesn't match the resolved sort", () => {
    const cursor = encodeCursor({
      field: "code",
      value: "X",
      id: "00000000-0000-0000-0000-000000000001",
      dir: "desc",
    });

    expect(() =>
      container.queryPlanner.planList(
        "test.widgets",
        { limit: 10, sort: "name", cursor },
        { tenantId: "00000000-0000-0000-0000-000000000001" },
      ),
    ).toThrow(InvalidCursorError);
  });

  it("throws InvalidCursorError for a garbage cursor string", () => {
    expect(() =>
      container.queryPlanner.planList(
        "test.widgets",
        { limit: 10, cursor: "not-a-real-cursor" },
        { tenantId: "00000000-0000-0000-0000-000000000001" },
      ),
    ).toThrow(InvalidCursorError);
  });
});

describe("QueryPlanner keyset pagination (via CrudService.list, live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000050",
    userId: "00000000-0000-0000-0000-000000000051",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-pagination-test-"));
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
    container.metadata.register(testWidgetEntity);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping keyset pagination live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { code: "P001", name: "Page Alpha" },
      { code: "P002", name: "Page Bravo" },
      { code: "P003", name: "Page Charlie" },
      { code: "P004", name: "Page Delta" },
      { code: "P005", name: "Page Echo" },
    ];

    for (const seed of seedRecords) {
      const created = await container.crud.create("test.widgets", seed, context);
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

  it("pages through the default sort (createdAt) with no overlap or gaps", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("test.widgets", { limit: 2 }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    expect(page1.data).toHaveLength(2);
    const cursor1 = (page1.page as { nextCursor: string | null }).nextCursor;
    expect(cursor1).not.toBeNull();

    const page2 = await container.crud.list(
      "test.widgets",
      { limit: 2, cursor: cursor1 as string },
      context,
    );
    expect(page2.ok).toBe(true);
    if (!page2.ok) return;
    expect(page2.data).toHaveLength(2);

    const idsSoFar = [...page1.data.map((r) => r.id), ...page2.data.map((r) => r.id)];
    expect(new Set(idsSoFar).size).toBe(4);
    expect(idsSoFar.every((id) => createdIds.includes(id))).toBe(true);
  });

  it("pages through a JSONB-backed sort field (name)", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("test.widgets", { limit: 2, sort: "name" }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    const names1 = page1.data.map((r) => (r.data as { name?: string }).name);
    const cursor1 = (page1.page as { nextCursor: string | null }).nextCursor;
    expect(cursor1).not.toBeNull();

    const page2 = await container.crud.list(
      "test.widgets",
      { limit: 2, sort: "name", cursor: cursor1 as string },
      context,
    );
    expect(page2.ok).toBe(true);
    if (!page2.ok) return;
    const names2 = page2.data.map((r) => (r.data as { name?: string }).name);

    expect(names1).toEqual(["Page Alpha", "Page Bravo"]);
    expect(names2).toEqual(["Page Charlie", "Page Delta"]);
  });

  it("rejects a cursor generated under a different sort with 400 invalid_cursor", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("test.widgets", { limit: 2 }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    const cursorFromDefaultSort = (page1.page as { nextCursor: string | null })
      .nextCursor as string;

    const result = await container.crud.list(
      "test.widgets",
      { limit: 2, sort: "name", cursor: cursorFromDefaultSort },
      context,
    );

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.status).toBe(400);
    expect(result.error).toBe("invalid_cursor");
  });

  it("rejects a garbage cursor string with 400 invalid_cursor, not a 500", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.widgets",
      { limit: 2, cursor: "not-a-real-cursor" },
      context,
    );

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.status).toBe(400);
    expect(result.error).toBe("invalid_cursor");
  });

  it("returns nextCursor: null on the last page", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list("test.widgets", { limit: 30 }, context);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect((result.page as { nextCursor: string | null }).nextCursor).toBeNull();
  });
});
