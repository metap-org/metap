import { createContainer, loadConfig, runOutboxPublisherLoop } from "@metap/core";
import { registerEntities } from "../modules/registry";

const config = loadConfig();
const container = createContainer(config);
registerEntities(container.metadata);

try {
  await runOutboxPublisherLoop(container);
} finally {
  await container.close();
}
