import { sql } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "./client";
import type { Database } from "./client";
import { assertCoreSchemaPresent } from "./schema-check";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("assertCoreSchemaPresent (live DB)", () => {
  let db: Database;
  let dbAvailable = true;

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    try {
      await db.client.execute(sql`select 1`);
    } catch {
      dbAvailable = false;
    }
  });

  afterAll(async () => {
    await db.close();
  });

  it("resolves without throwing against a real, migrated database", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    await expect(assertCoreSchemaPresent(db)).resolves.toBeUndefined();
  });

  it("throws naming the missing tables when pointed at a database without packages/core's schema", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    // Reuses the same running Postgres server/credentials, just the default
    // "postgres" maintenance database — always exists, never has this
    // codebase's tables. A clean way to exercise "connected, but the wrong
    // database" without any schema-juggling.
    const wrongUrl = databaseUrl.replace(/\/[^/]+$/, "/postgres");
    const wrongDb = createDatabase(wrongUrl);

    try {
      await expect(assertCoreSchemaPresent(wrongDb)).rejects.toThrow();

      try {
        await assertCoreSchemaPresent(wrongDb);
        expect.unreachable("expected assertCoreSchemaPresent to throw");
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        expect(message).toContain("records");
        expect(message).toContain("policies");
        expect(message).toContain("outbox_events");
      }
    } finally {
      await wrongDb.close();
    }
  });
});
