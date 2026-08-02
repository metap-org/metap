import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { outboxEvents } from "../../infra/db/schema";
import type { RabbitPublisher } from "../../infra/messaging/rabbitmq";
import { OutboxService } from "./outbox-service";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("OutboxService.publishPending (live DB)", () => {
  let db: Database;
  let pgClient: Client;
  let dbAvailable = true;

  beforeAll(async () => {
    db = createDatabase(databaseUrl);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping OutboxService live-DB tests: could not connect to ${databaseUrl}: ${
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

  async function seedPending(count: number): Promise<string[]> {
    const aggregateId = "00000000-0000-0000-0000-000000000001";
    const rows = await db.client
      .insert(outboxEvents)
      .values(
        Array.from({ length: count }, (_, i) => ({
          topic: `test.event.${i}`,
          aggregateType: "test.widgets",
          aggregateId,
          payload: { i },
        })),
      )
      .returning({ id: outboxEvents.id, topic: outboxEvents.topic });

    return rows.map((r) => r.id);
  }

  async function cleanup(ids: string[]) {
    for (const id of ids) {
      await pgClient.query("DELETE FROM outbox_events WHERE id = $1", [id]);
    }
  }

  it("never publishes the same row twice when publishPending() runs concurrently", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const ids = await seedPending(6);
    const publishedTopics: string[] = [];
    const rabbit: RabbitPublisher = {
      // A small artificial delay widens the race window enough that, without
      // row locking, two concurrent publishPending() calls reliably both
      // select overlapping rows before either transaction commits — without
      // this, the bug this test guards against is too timing-dependent to
      // reproduce deterministically.
      publish: vi.fn(async (topic: string) => {
        await new Promise((resolve) => setTimeout(resolve, 30));
        publishedTopics.push(topic);
      }),
      close: vi.fn(async () => {}),
    };
    const service = new OutboxService(db, rabbit);

    try {
      await Promise.all([service.publishPending(3), service.publishPending(3)]);

      const uniqueTopics = new Set(publishedTopics);
      expect(publishedTopics.length).toBe(uniqueTopics.size);
      expect(uniqueTopics.size).toBe(6);
    } finally {
      await cleanup(ids);
    }
  });

  it("marks a row published and records attempts/lastError on a simulated failure", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const ids = await seedPending(1);
    const rabbit: RabbitPublisher = {
      publish: vi.fn().mockRejectedValueOnce(new Error("simulated publish failure")),
      close: vi.fn(async () => {}),
    };
    const service = new OutboxService(db, rabbit);

    try {
      await service.publishPending(10);

      const row = await pgClient.query<{ attempts: number; last_error: string | null }>(
        "SELECT attempts, last_error FROM outbox_events WHERE id = $1",
        [ids[0]],
      );
      expect(row.rows[0]?.attempts).toBe(1);
      expect(row.rows[0]?.last_error).toBe("simulated publish failure");

      await service.publishPending(10);
      const row2 = await pgClient.query<{ published_at: Date | null }>(
        "SELECT published_at FROM outbox_events WHERE id = $1",
        [ids[0]],
      );
      expect(row2.rows[0]?.published_at).not.toBeNull();
    } finally {
      await cleanup(ids);
    }
  });
});
