import { and, eq, isNull, sql } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
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

  async enqueue(event: OutboxEvent) {
    await this.db.client.insert(outboxEvents).values(event);
  }

  async publishPending(limit = 100) {
    const pending = await this.db.client
      .select()
      .from(outboxEvents)
      .where(isNull(outboxEvents.publishedAt))
      .orderBy(outboxEvents.createdAt)
      .limit(limit);

    for (const event of pending) {
      try {
        await this.rabbit.publish(event.topic, event.payload);
        await this.db.client
          .update(outboxEvents)
          .set({ publishedAt: new Date(), lastError: null })
          .where(eq(outboxEvents.id, event.id));
      } catch (error) {
        await this.db.client
          .update(outboxEvents)
          .set({
            attempts: sql`${outboxEvents.attempts} + 1`,
            lastError: error instanceof Error ? error.message : String(error),
          })
          .where(and(eq(outboxEvents.id, event.id), isNull(outboxEvents.publishedAt)));
      }
    }
  }
}
