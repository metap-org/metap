import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { MetadataDriftService } from "./metadata-drift";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("MetadataDriftService (live DB)", () => {
  let db: Database;
  let service: MetadataDriftService;
  let pgClient: Client;
  let dbAvailable = true;

  const entityName = "test.drift-check-entity";

  function makeLogSpy() {
    const messages: string[] = [];
    return { warn: (_obj: unknown, msg: string) => messages.push(msg), messages };
  }

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    service = new MetadataDriftService(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping MetadataDriftService live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.query("DELETE FROM metadata_versions WHERE entity_name = $1", [entityName]);
      await pgClient.end();
    }
    await db.close();
  });

  it("logs 'first boot' on the first call, nothing on an unchanged second call, drift on a changed third call", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    await pgClient.query("DELETE FROM metadata_versions WHERE entity_name = $1", [entityName]);

    const first = makeLogSpy();
    await service.check([{ name: entityName, version: "hash-a" }], first);
    expect(first.messages).toEqual(["metadata: first boot, recording initial hash"]);

    const second = makeLogSpy();
    await service.check([{ name: entityName, version: "hash-a" }], second);
    expect(second.messages).toEqual([]);

    const third = makeLogSpy();
    await service.check([{ name: entityName, version: "hash-b" }], third);
    expect(third.messages).toEqual(["metadata: drift detected since last boot"]);
  });

  it("does not throw when the database is unreachable", async () => {
    const unreachableDb = createDatabase("postgres://x:x@localhost:1/x");
    const unreachableService = new MetadataDriftService(unreachableDb);
    const spy = makeLogSpy();

    await expect(
      unreachableService.check([{ name: entityName, version: "hash-a" }], spy),
    ).resolves.toBeUndefined();
    expect(spy.messages).toEqual(["metadata: drift check skipped, could not reach the database"]);

    await unreachableDb.close();
  });
});
