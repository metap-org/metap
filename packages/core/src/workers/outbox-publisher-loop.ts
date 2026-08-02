import { setTimeout as sleep } from "node:timers/promises";
import type { AppContainer } from "../core/container";

export async function runOutboxPublisherLoop(
  container: AppContainer,
  options?: { pollMs?: number; batchSize?: number },
): Promise<void> {
  const pollMs = options?.pollMs ?? 1000;
  const batchSize = options?.batchSize ?? 100;

  let closing = false;

  const stop = () => {
    closing = true;
  };
  process.on("SIGINT", stop);
  process.on("SIGTERM", stop);

  try {
    while (!closing) {
      await container.outbox.publishPending(batchSize);
      await sleep(pollMs);
    }
  } finally {
    process.off("SIGINT", stop);
    process.off("SIGTERM", stop);
  }
}
