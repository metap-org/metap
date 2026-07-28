import { setTimeout as sleep } from "node:timers/promises";
import { createContainer } from "../core/container";
import { loadConfig } from "../server/config";

const config = loadConfig();
const container = createContainer(config);

let closing = false;

process.on("SIGINT", () => {
  closing = true;
});

process.on("SIGTERM", () => {
  closing = true;
});

try {
  while (!closing) {
    await container.outbox.publishPending(100);
    await sleep(1000);
  }
} finally {
  await container.close();
}
