import { and, eq, isNull, sql } from "drizzle-orm";
import type { DbExecutor, Database } from "../../infra/db/client";
import { outboxEvents } from "../../infra/db/schema";
import type { RabbitPublisher } from "../../infra/messaging/rabbitmq";

export type OutboxEvent = {
  topic: string;
  aggregateType: string;
  aggregateId: string;
  payload: unknown;
};

export class OutboxService {
  constructor(
    private readonly db: Database,
    private readonly rabbit: RabbitPublisher,
  ) {}

  async enqueue(executor: DbExecutor, event: OutboxEvent) {
    await executor.insert(outboxEvents).values(event);
  }

  async publishPending(limit = 100) {
    // SELECT ... FOR UPDATE SKIP LOCKED, held open for the whole
    // publish-then-mark-done cycle: a concurrent publishPending() call (a
    // second outbox worker) skips whatever rows this transaction has locked
    // instead of blocking or re-selecting them, so two workers never publish
    // the same row twice.
    await this.db.client.transaction(async (tx) => {
      const pending = await tx
        .select()
        .from(outboxEvents)
        .where(isNull(outboxEvents.publishedAt))
        .orderBy(outboxEvents.createdAt)
        .limit(limit)
        .for("update", { skipLocked: true });

      for (const event of pending) {
        try {
          await this.rabbit.publish(event.topic, event.payload);
          await tx
            .update(outboxEvents)
            .set({ publishedAt: new Date(), lastError: null })
            .where(eq(outboxEvents.id, event.id));
        } catch (error) {
          await tx
            .update(outboxEvents)
            .set({
              attempts: sql`${outboxEvents.attempts} + 1`,
              lastError: error instanceof Error ? error.message : String(error),
            })
            .where(and(eq(outboxEvents.id, event.id), isNull(outboxEvents.publishedAt)));
        }
      }
    });
  }
}
